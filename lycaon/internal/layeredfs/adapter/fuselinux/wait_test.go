//go:build linux

package fuselinux_test

import (
	"testing"
	"time"
)

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
