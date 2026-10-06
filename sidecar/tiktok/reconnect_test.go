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

// TestJitteredStaysInBounds: jitter must not turn a 2s blip retry into
// something the user notices, and must not exceed the step it decorates.
func TestJitteredStaysInBounds(t *testing.T) {
	for _, step := range []time.Duration{2 * time.Second, 10 * time.Second, time.Minute} {
		seen := map[time.Duration]bool{}
		for i := 0; i < 200; i++ {
			got := jittered(step)
			if got < step/2 || got > step {
				t.Fatalf("jittered(%s) = %s, want within [%s, %s]", step, got, step/2, step)
			}
			seen[got] = true
		}
		if len(seen) < 2 {
			t.Errorf("jittered(%s) produced %d distinct values; want variation", step, len(seen))
		}
	}
}

// TestJitteredZero: a zero/negative delay must pass through untouched, not panic.
func TestJitteredZero(t *testing.T) {
	if got := jittered(0); got != 0 {
		t.Errorf("jittered(0) = %s, want 0", got)
	}
}

// TestShortDuration: the status line must read "5s"/"2m", not Go's "2m0s".
func TestShortDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{5 * time.Second, "5s"},
		{30 * time.Second, "30s"},
		{time.Minute, "1m"},
		{2 * time.Minute, "2m"},
		{10 * time.Minute, "10m"},
	}
	for _, c := range cases {
		if got := shortDuration(c.in); got != c.want {
			t.Errorf("shortDuration(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestBlockedBackoffLadder: the refusal ladder must start SHORT (a blip should
// recover in seconds, not make the user wait a minute) and only then escalate
// past the ordinary reconnect cap, because a real rate limit needs minutes of
// quiet. Both halves matter: too eager and a real limit never clears; too slow
// at the start and a blip costs the user a long wait.
func TestBlockedBackoffLadder(t *testing.T) {
	steps := blockedBackoffSteps
	if len(steps) == 0 {
		t.Fatal("blockedBackoffSteps is empty")
	}
	if steps[0] > 10*time.Second {
		t.Errorf("first refusal waits %s; a blip must recover fast", steps[0])
	}
	if steps[0] > reconnectBaseDelay*2 {
		t.Errorf("first refusal waits %s, longer than a normal reconnect", steps[0])
	}
	// Strictly increasing, and the last step must outlast the reconnect cap.
	for i := 1; i < len(steps); i++ {
		if steps[i] <= steps[i-1] {
			t.Errorf("step %d (%s) does not exceed step %d (%s)",
				i, steps[i], i-1, steps[i-1])
		}
	}
	if last := steps[len(steps)-1]; last <= reconnectMaxDelay {
		t.Errorf("final step %s does not exceed the reconnect cap %s",
			last, reconnectMaxDelay)
	}
	if blockedGiveUpAfter != len(steps) {
		t.Errorf("blockedGiveUpAfter = %d, want len(steps) = %d",
			blockedGiveUpAfter, len(steps))
	}
}

// TestSleepForAbortsOnCancel: quitting OBS (or reconnecting by hand) during a
// refusal wait must end it immediately, not sleep for minutes.
func TestSleepForAbortsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if sleepFor(ctx, 5*time.Minute) {
		t.Fatal("sleepFor returned true on a cancelled context")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("sleepFor waited %s after cancel; want an immediate return", elapsed)
	}
}

// TestSleepForWaitsWhenNotCancelled: the guard above must not be a no-op.
func TestSleepForWaitsWhenNotCancelled(t *testing.T) {
	start := time.Now()
	if !sleepFor(context.Background(), 20*time.Millisecond) {
		t.Fatal("sleepFor returned false on a live context")
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Fatalf("sleepFor returned after %s; want it to wait", elapsed)
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
