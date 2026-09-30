package main

import (
	"context"
	"errors"
	"testing"
	"time"

	gotiktoklive "github.com/steampoweredtaco/gotiktoklive"
)

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

// TestTerminalTrackError: a stream that ended (or an unknown handle) must NOT be
// retried, while transient failures must be.
func TestTerminalTrackError(t *testing.T) {
	terminal := []error{
		gotiktoklive.ErrUserOffline,
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
