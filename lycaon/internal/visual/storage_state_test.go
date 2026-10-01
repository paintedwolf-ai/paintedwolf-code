package visual

import (
	"os"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProjectStorageLocksReleaseIdleEntries(t *testing.T) {
	var registry projectStorageLockRegistry
	for i := 0; i < 64; i++ {
		registry.lock(strconv.Itoa(i))()
	}
	registry.mu.Lock()
	entries := len(registry.entries)
	registry.mu.Unlock()
	if entries != 0 {
		t.Fatalf("project storage locks = %d, want 0", entries)
	}
}

func TestArtifactRepairStatesStayBounded(t *testing.T) {
	var registry artifactRepairRegistry
	t.Cleanup(func() {
		registry.mu.Lock()
		defer registry.mu.Unlock()
		for key, entry := range registry.entries {
			entry.state.close()
			delete(registry.entries, key)
		}
	})
	root := t.TempDir()
	for i := 0; i < maxIdleArtifactRepairStates*2; i++ {
		state, release := registry.acquire(strconv.Itoa(i))
		dir, err := os.Open(root)
		testutil.FailErr(t, "open artifact directory", err)
		state.dir = dir
		release()
	}
	registry.mu.Lock()
	entries := len(registry.entries)
	registry.mu.Unlock()
	if entries > maxIdleArtifactRepairStates {
		t.Fatalf("artifact repair states = %d, want <= %d", entries, maxIdleArtifactRepairStates)
	}
}
