//go:build !darwin && !linux

package literal

import (
	"testing"
	"time"
)

func linkAt(t *testing.T, target, path string, _ time.Time) {
	t.Helper()
	symlink(t, target, path)
}

func ownerIDs(t *testing.T, _ string) (uint32, uint32) {
	t.Helper()
	return 0, 0
}
