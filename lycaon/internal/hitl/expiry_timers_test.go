package hitl

import (
	"testing"
	"time"
)

func TestExpiryTimersFireUntilStopped(t *testing.T) {
	var timers expiryTimers
	fired := make(chan struct{})
	timers.schedule(time.Millisecond, func() { close(fired) })
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("armed expiry never fired")
	}

	late := make(chan struct{})
	timers.schedule(time.Hour, func() { close(late) })
	timers.stop()
	timers.mu.Lock()
	armed := len(timers.timers)
	timers.mu.Unlock()
	if armed != 0 {
		t.Fatalf("%d expiries still armed after stop", armed)
	}

	timers.schedule(time.Millisecond, func() { close(late) })
	select {
	case <-late:
		t.Fatal("expiry scheduled after stop fired")
	case <-time.After(50 * time.Millisecond):
	}
	var manager *Manager
	manager.StopExpiryTimers()
}
