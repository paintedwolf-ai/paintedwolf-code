package testutil

import (
	"fmt"
	"io"
	"os"
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

// Progress is a system's work at one instant. Epoch changes whenever work
// begins, ends or decides anything; Idle means nothing in flight can still
// change the system; Report explains the state for a failure.
type Progress struct {
	Epoch  uint64
	Idle   bool
	Report string
}

// ProgressIdleWindow is how long a system must stay idle, with no work beginning,
// ending or deciding anything, before WaitForProgress gives up. It is long so
// that work the progress report does not track cannot fail a healthy test; the
// run backstop still bounds every wait.
const ProgressIdleWindow = 2 * time.Minute

// WaitForProgress polls cond like WaitFor and fails once the system has stayed
// idle at one epoch for ProgressIdleWindow with the condition still false. Each
// idle reading brackets a condition check at an unchanged epoch, so no work ran
// in between; any busy reading or epoch change restarts the window.
func WaitForProgress(t testing.TB, step string, cond func() bool, progress func() Progress) {
	t.Helper()
	waitForProgress(t, step, cond, progress, time.Now)
}

func waitForProgress(t testing.TB, step string, cond func() bool, progress func() Progress, now func() time.Time) {
	t.Helper()
	defer trackWait(t, step)()
	ticker := time.NewTicker(waitForPollInterval)
	defer ticker.Stop()
	var idleSince time.Time
	var idleEpoch uint64
	for {
		before := progress()
		if cond() {
			return
		}
		after := progress()
		switch {
		case !before.Idle || !after.Idle || after.Epoch != before.Epoch:
			idleSince = time.Time{}
		case idleSince.IsZero() || after.Epoch != idleEpoch:
			idleSince, idleEpoch = now(), after.Epoch
		case now().Sub(idleSince) >= ProgressIdleWindow:
			t.Fatalf("%s: the system stayed idle for %s with nothing left that could make the condition hold\n%s",
				step, ProgressIdleWindow, after.Report)
		}
		select {
		case <-ticker.C:
		case <-t.Context().Done():
			t.Fatalf("%s: the test ended before the condition held\n%s", step, progress().Report)
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
	// out is the test binary's stderr as a test goroutine sees it, read after
	// testing.M.Run has settled it.
	out io.Writer
}{entries: map[uint64]string{}}

// trackWait lists a wait as pending until the returned function runs.
func trackWait(t testing.TB, step string) func() {
	pendingWaits.Lock()
	defer pendingWaits.Unlock()
	if pendingWaits.out == nil {
		pendingWaits.out = os.Stderr
	}
	id := pendingWaits.next
	pendingWaits.next++
	pendingWaits.entries[id] = t.Name() + ": " + step
	return func() {
		pendingWaits.Lock()
		defer pendingWaits.Unlock()
		delete(pendingWaits.entries, id)
	}
}

func pendingWaitsOutput(fallback io.Writer) io.Writer {
	pendingWaits.Lock()
	defer pendingWaits.Unlock()
	if pendingWaits.out == nil {
		return fallback
	}
	return pendingWaits.out
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
