package cadence

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/rootfacts"
)

// Scans serve agents, so no entry point schedules one for an editor-only root.
func TestEditorOnlyRootsAreNeverScheduled(t *testing.T) {
	facts := rootfacts.Install(t)
	cadence, store := newTestCadence(t, newTestClock())
	ctx := t.Context()
	for _, root := range facts.EditorOnly() {
		testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root.Path, "main.go"), []byte("package main\n"), 0o600))
		testutil.FailErr(t, "baseline", cadence.BaselineRoot(ctx, root.Path))
		testutil.FailErr(t, "inventory", cadence.NoteTree(ctx, root.Path, 1))
		testutil.FailErr(t, "write", noteCadenceWrites(ctx, cadence, root.Path, []string{"main.go"}))
		rows, err := store.ListSeriesForPath(ctx, root.Path)
		testutil.FailErr(t, "series", err)
		if len(rows) != 0 {
			t.Fatalf("%s: %d scan series for an editor-only root", root.Name, len(rows))
		}
	}
	testutil.FailErr(t, "admitted baseline", cadence.BaselineRoot(ctx, facts.Work))
	rows, err := store.ListSeriesForPath(ctx, facts.Work)
	testutil.FailErr(t, "admitted series", err)
	if len(rows) == 0 {
		t.Fatal("an admitted root has no scan series")
	}
}
