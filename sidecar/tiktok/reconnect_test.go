package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	gotiktoklive "github.com/steampoweredtaco/gotiktoklive"
)

// replayChat wraps a real ChatEvent but reports IsHistory()==true, the shape
// TikTok's replayed backlog takes on a (re)connect. The real flag is
// unexported, so the test overrides the method on a wrapper.
type replayChat struct{ gotiktoklive.ChatEvent }

func (r replayChat) IsHistory() bool { return true }

// liveChat reports IsHistory()==false: a genuinely new message.
type liveChat struct{ gotiktoklive.ChatEvent }

func (l liveChat) IsHistory() bool { return false }

// TestNextBackoffDoublesAndCaps: the retry delay must grow exponentially and
// stop at the cap, so a long outage does not turn into a tight reconnect loop.
func TestNextBackoffDoublesAndCaps(t *testing.T) {
	cases := []struct{ in, want time.Duration }{
		{reconnectBaseDelay, reconnectBaseDelay * 2},
		{reconnectBaseDelay * 2, reconnectBaseDelay * 4},
		{reconnectBaseDelay * 4, reconnectBaseDelay * 8},
		{reconnectBaseDelay * 8, reconnectMaxDelay},  // 8s*2=16s
		{reconnectBaseDelay * 16, reconnectMaxDelay}, // 32s clamped
		{reconnectMaxDelay, reconnectMaxDelay},
		{reconnectMaxDelay * 4, reconnectMaxDelay},
	}
	for _, c := range cases {
		if got := nextBackoff(c.in); got != c.want {
			t.Errorf("nextBackoff(%s) = %s, want %s", c.in, got, c.want)
		}
	}
}

// TestSleepBackoffAbortsOnCancel: a connect/disconnect/quit during the wait must
// end the retry loop immediately instead of sleeping through the whole delay.
func TestSleepBackoffAbortsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	delay := reconnectBaseDelay
	start := time.Now()
	if sleepBackoff(ctx, &delay) {
		t.Fatal("sleepBackoff returned true on a cancelled context")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("sleepBackoff waited %s after cancel; want an immediate return", elapsed)
	}
}

// TestTerminalTrackError: only an unresolvable handle stops the session for
// good. A stream that is not live (yet) or just ended must keep the sidecar
// polling, or opening OBS before going live would require an OBS restart.
func TestTerminalTrackError(t *testing.T) {
	terminal := []error{
		gotiktoklive.ErrUserNotFound,
		gotiktoklive.ErrUserInfoNotFound,
	}
	for _, err := range terminal {
		if !terminalTrackError(err) {
			t.Errorf("terminalTrackError(%v) = false, want true", err)
		}
	}
	transient := []error{
		errors.New("failed to read websocket from server: EOF"),
		gotiktoklive.ErrRateLimitExceeded,
		gotiktoklive.ErrCaptcha,
	}
	for _, err := range transient {
		if terminalTrackError(err) {
			t.Errorf("terminalTrackError(%v) = true, want false (should retry)", err)
		}
	}
}

// TestNotLiveError: "not live yet" and "stream ended" are the wait-and-poll
// cases — not terminal — so the next stream in the same OBS session connects.
func TestNotLiveError(t *testing.T) {
	notLive := []error{
		gotiktoklive.ErrUserOffline,
		gotiktoklive.ErrLiveHasEnded,
	}
	for _, err := range notLive {
		if !notLiveError(err) {
			t.Errorf("notLiveError(%v) = false, want true", err)
		}
		if terminalTrackError(err) {
			t.Errorf("terminalTrackError(%v) = true, want false (must poll, not give up)", err)
		}
	}
	other := []error{
		gotiktoklive.ErrUserNotFound,
		errors.New("failed to read websocket from server: EOF"),
	}
	for _, err := range other {
		if notLiveError(err) {
			t.Errorf("notLiveError(%v) = true, want false", err)
		}
	}
}

// TestWaitForLivePollAbortsOnCancel: a connect/disconnect/quit during the wait
// must end the poll loop immediately, not sleep through the whole interval.
func TestWaitForLivePollAbortsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if waitForLivePoll(ctx) {
		t.Fatal("waitForLivePoll returned true on a cancelled context")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waitForLivePoll waited %s after cancel; want an immediate return", elapsed)
	}
}

// captureEmit runs fn with os.Stdout redirected and returns what emit wrote.
func captureEmit(fn func()) string {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

// TestForwardableDropsReplayedHistory: a frame the library tagged as history
// must never reach a widget, or the same questions re-enter the queue on every
// reconnect. A live frame must still be forwarded.
func TestForwardableDropsReplayedHistory(t *testing.T) {
	base := gotiktoklive.ChatEvent{Comment: "!q question"}
	if forwardable(replayChat{ChatEvent: base}) {
		t.Error("forwardable(replay) = true, want false (replay must be dropped)")
	}
	if !forwardable(liveChat{ChatEvent: base}) {
		t.Error("forwardable(live) = false, want true (live must be forwarded)")
	}
}

// TestHandleEventForwardsLiveChat proves a genuinely new chat frame still
// reaches the emit path after the replay guard was added. (The replay branch is
// covered by TestForwardableDropsReplayedHistory: a wrapper type cannot match
// handleEvent's concrete type switch, so asserting "nothing emitted" for it
// would pass even with the guard removed.)
func TestHandleEventForwardsLiveChat(t *testing.T) {
	user := &gotiktoklive.User{Nickname: "Viewer", Username: "viewer"}
	live := gotiktoklive.ChatEvent{Comment: "!q live question", User: user}

	out := captureEmit(func() { handleEvent(live) })
	if !strings.Contains(out, "live question") {
		t.Errorf("live frame was dropped, want forwarded; got %q", out)
	}
	if !strings.Contains(out, `"event":"chat"`) {
		t.Errorf("live frame emitted with wrong shape; got %q", out)
	}
}
