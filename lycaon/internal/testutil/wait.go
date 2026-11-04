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

// failureBound turns a hang into a test failure; it decides no behavior.
const failureBound = 2 * time.Minute

// AwaitClosed fails the test rather than hanging if ch never closes.
func AwaitClosed[T any](t testing.TB, step string, ch <-chan T) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(failureBound):
		t.Fatalf("%s: channel did not close", step)
	}
}

// AwaitValue fails the test rather than hanging if ch never delivers.
func AwaitValue[T any](t testing.TB, step string, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(failureBound):
		t.Fatalf("%s: no value arrived", step)
		var zero T
		return zero
	}
}

// AwaitCondition fails the test rather than hanging if cond never holds.
func AwaitCondition(t testing.TB, step string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(failureBound)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: condition never held", step)
		}
		time.Sleep(waitForPollInterval)
	}
}
