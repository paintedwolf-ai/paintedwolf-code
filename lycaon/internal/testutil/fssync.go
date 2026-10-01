package testutil

import "github.com/lycaon/lycaon/internal/fssync"

// Test binaries never prove power-loss durability, so the flush is skipped.
func init() {
	fssync.Relax()
}
