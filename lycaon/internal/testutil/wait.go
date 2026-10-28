package testutil

import (
	"testing"
	"time"
)

const waitForPollInterval = 5 * time.Millisecond

// WaitFor polls cond until it returns true or the test context ends.
func WaitFor(t *testing.T, cond func() bool) {
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
