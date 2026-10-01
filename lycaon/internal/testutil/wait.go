package testutil

import (
	"context"
	"testing"
	"time"
)

const waitForPollInterval = 5 * time.Millisecond

// WaitFor polls cond until it returns true or timeout elapses.
func WaitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	if !WaitForNoFatal(timeout, cond) {
		t.Fatal("timed out waiting for condition")
	}
}

// WaitForNoFatal reports timeout without failing the test.
func WaitForNoFatal(timeout time.Duration, cond func() bool) bool {
	timeout = Timeout(timeout)
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		if remaining > waitForPollInterval {
			remaining = waitForPollInterval
		}
		time.Sleep(remaining)
	}
}

// Receive returns the next channel value within the test timeout.
func Receive[T any](t testing.TB, step string, ch <-chan T) T {
	t.Helper()
	timer := time.NewTimer(Timeout(2 * time.Second))
	defer timer.Stop()
	select {
	case value, ok := <-ch:
		if !ok {
			t.Fatalf("%s: channel closed before a value arrived", step)
		}
		return value
	case <-timer.C:
		t.Fatalf("%s: timed out waiting for channel value", step)
		var zero T
		return zero
	}
}

// BoundedContext returns a test-scoped context with a deadline.
func BoundedContext(t testing.TB, timeout time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), Timeout(timeout))
	t.Cleanup(cancel)
	return ctx
}
