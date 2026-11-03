package fuselinux_test

import (
	"runtime/debug"
	"testing"
	"time"
)

// copyToMapping writes to a mapping of the mount from this process. A page fault cannot be
// preempted and needs a server reply, so a stop-the-world during it would hang the test binary.
func copyToMapping(mapped, data []byte) {
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
