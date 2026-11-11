//go:build linux

package fuselinux_test

import (
	"runtime"
	"runtime/debug"
	"testing"
	"time"
)

// copyToMapping writes to a mapping of the mount from this process. A page fault cannot be
// preempted and needs a server reply, so it needs another scheduler slot and no stop-the-world.
// The engine never maps mount files; only these tests do.
func copyToMapping(mapped, data []byte) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(max(4, runtime.GOMAXPROCS(0))))
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	copy(mapped, data)
}

// awaitCondition blocks until cond holds. The outcome never depends on elapsed time: the go test
// deadline only turns a genuine hang into a labeled failure.
func awaitCondition(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline, bounded := t.Deadline()
	for !cond() {
		if bounded && time.Now().After(deadline.Add(-5*time.Second)) {
			t.Fatalf("never observed: %s", desc)
		}
		time.Sleep(time.Millisecond)
	}
}
