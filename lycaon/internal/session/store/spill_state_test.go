package store

import (
	"os"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSpillRepairStatesStayBounded(t *testing.T) {
	var registry spillRepairRegistry
	t.Cleanup(func() {
		registry.mu.Lock()
		defer registry.mu.Unlock()
		for key, entry := range registry.entries {
			entry.state.close()
			delete(registry.entries, key)
		}
	})
	root := t.TempDir()
	for i := 0; i < maxIdleSpillRepairStates*2; i++ {
		state, release := registry.acquire(strconv.Itoa(i))
		dir, err := os.Open(root)
		testutil.FailErr(t, "open spill directory", err)
		state.dir = dir
		release()
	}
	registry.mu.Lock()
	entries := len(registry.entries)
	registry.mu.Unlock()
	if entries > maxIdleSpillRepairStates {
		t.Fatalf("spill repair states = %d, want <= %d", entries, maxIdleSpillRepairStates)
	}
}
