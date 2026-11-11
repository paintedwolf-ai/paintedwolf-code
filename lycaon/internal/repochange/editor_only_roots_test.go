package repochange

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil/rootfacts"
)

// No watcher registration exists for an editor-only root, so the
// engine never observes its own writes or a broad tree's churn.
func TestEditorOnlyRootsAreNeverWatched(t *testing.T) {
	facts := rootfacts.Install(t)
	reg := NewWatcherRegistry()
	t.Cleanup(reg.CloseAll)
	for _, root := range facts.EditorOnly() {
		reg.Ensure(t.Context(), root.Path)
	}
	reg.Ensure(t.Context(), facts.Work)
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if len(reg.byDir) != 1 {
		t.Fatalf("watchers = %d, want only the admitted root", len(reg.byDir))
	}
	for dir := range reg.byDir {
		if dir != facts.Work {
			t.Fatalf("watched %s, want %s", dir, facts.Work)
		}
	}
}
