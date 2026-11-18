package testutil

import (
	"fmt"
	"io"
	"slices"
	"sync"
	"testing"
	"time"
)

const waitForPollInterval = 5 * time.Millisecond

// WaitFor polls cond until it holds. Like every wait here it ends only with the test:
// the run's backstop reports a wait that never finishes by its test and step.
func WaitFor(t testing.TB, step string, cond func() bool) {
	t.Helper()
	defer trackWait(t, step)()
	ticker := time.NewTicker(waitForPollInterval)
	defer ticker.Stop()
	for !cond() {
		select {
		case <-ticker.C:
		case <-t.Context().Done():
			t.Fatalf("%s: the test ended before the condition held", step)
		}
	}
}

// Receive returns the next channel value.
func Receive[T any](t testing.TB, step string, ch <-chan T) T {
	t.Helper()
	defer trackWait(t, step)()
	select {
	case value, ok := <-ch:
		if !ok {
			t.Fatalf("%s: channel closed before a value arrived", step)
		}
		return value
	case <-t.Context().Done():
		t.Fatalf("%s: the test ended before a value arrived", step)
		var zero T
		return zero
	}
}

// AwaitClosed returns once ch closes or delivers a value.
func AwaitClosed[T any](t testing.TB, step string, ch <-chan T) {
	t.Helper()
	defer trackWait(t, step)()
	select {
	case <-ch:
	case <-t.Context().Done():
		t.Fatalf("%s: the test ended before the channel closed", step)
	}
}

var pendingWaits = struct {
	sync.Mutex
	next    uint64
	entries map[uint64]string
}{entries: map[uint64]string{}}

// trackWait lists a wait as pending until the returned function runs.
func trackWait(t testing.TB, step string) func() {
	pendingWaits.Lock()
	defer pendingWaits.Unlock()
	id := pendingWaits.next
	pendingWaits.next++
	pendingWaits.entries[id] = t.Name() + ": " + step
	return func() {
		pendingWaits.Lock()
		defer pendingWaits.Unlock()
		delete(pendingWaits.entries, id)
	}
}

// writePendingWaits prints one line per wait in progress, oldest first. The runner's
// digest reads the "pending wait: <test>: <step>" form.
func writePendingWaits(w io.Writer) {
	pendingWaits.Lock()
	ids := make([]uint64, 0, len(pendingWaits.entries))
	for id := range pendingWaits.entries {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	lines := make([]string, 0, len(ids))
	for _, id := range ids {
		lines = append(lines, pendingWaits.entries[id])
	}
	pendingWaits.Unlock()
	if len(lines) == 0 {
		fmt.Fprintln(w, "pending waits: none")
		return
	}
	for _, line := range lines {
		fmt.Fprintf(w, "pending wait: %s\n", line)
	}
}
