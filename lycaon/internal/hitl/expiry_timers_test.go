package hitl

import (
	"context"
	"testing"
	"time"
)

func TestExpiryTimersFireUntilStopped(t *testing.T) {
	var timers expiryTimers
	fired := make(chan string, 1)
	expire := func(_ context.Context, checkpointID, reason string) error {
		fired <- checkpointID + ": " + reason
		return nil
	}
	timers.schedule(t.Context(), "disabled", 0, expire)
	timers.schedule(t.Context(), "first", time.Millisecond, expire)
	select {
	case got := <-fired:
		if got != "first: "+expiryReason {
			t.Fatalf("expiry fired %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("armed expiry never fired")
	}

	timers.schedule(t.Context(), "pending", time.Hour, expire)
	timers.stop()
	timers.mu.Lock()
	armed := len(timers.timers)
	timers.mu.Unlock()
	if armed != 0 {
		t.Fatalf("%d expiries still armed after stop", armed)
	}

	timers.schedule(t.Context(), "late", time.Millisecond, expire)
	select {
	case got := <-fired:
		t.Fatalf("expiry %q fired after stop", got)
	case <-time.After(50 * time.Millisecond):
	}
	var manager *Manager
	manager.StopExpiryTimers()
}
