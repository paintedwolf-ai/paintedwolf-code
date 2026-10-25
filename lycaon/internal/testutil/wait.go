package testutil

import (
	"context"
	"testing"
	"time"
)

const waitForPollInterval = 5 * time.Millisecond

// WaitFor polls cond until it returns true or the test context ends.
func WaitFor(t *testing.T, _ time.Duration, cond func() bool) {
	t.Helper()
	for t.Context().Err() == nil {
		if cond() {
			return
		}
		time.Sleep(waitForPollInterval)
	}
	if !cond() {
		t.Fatal("timed out waiting for condition")
	}
}

// WaitForNoFatal polls cond until it returns true.
func WaitForNoFatal(_ time.Duration, cond func() bool) bool {
	for !cond() {
		time.Sleep(waitForPollInterval)
	}
	return true
}

// Receive returns the next channel value within the test timeout.
func Receive[T any](t testing.TB, step string, ch <-chan T) T {
	t.Helper()
	select {
	case value, ok := <-ch:
		if !ok {
			t.Fatalf("%s: channel closed before a value arrived", step)
		}
		return value
	case <-t.Context().Done():
		t.Fatalf("%s: timed out waiting for channel value", step)
		var zero T
		return zero
	}
}

// BoundedContext returns the test-scoped context.
func BoundedContext(t testing.TB, _ time.Duration) context.Context {
	t.Helper()
	return t.Context()
}
