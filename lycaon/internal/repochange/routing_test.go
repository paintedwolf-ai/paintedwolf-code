package repochange

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDebouncerCoalescesSymlinkAliasesAndPreservesResync(t *testing.T) {
	t.Cleanup(func() { ResetDebouncerForTest(context.Background()) })

	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	aliasDir := filepath.Join(base, "alias")
	testutil.FailErr(t, "mkdir real", os.MkdirAll(filepath.Join(realDir, "sub"), 0o755))
	testutil.FailErr(t, "symlink alias", os.Symlink(realDir, aliasDir))

	startEpoch := CurrentEpoch(realDir)
	if aliasEpoch := CurrentEpoch(aliasDir); aliasEpoch != startEpoch {
		t.Fatalf("symlink alias epoch %v != real epoch %v", aliasEpoch, startEpoch)
	}

	var count atomic.Int32
	var delivered Event
	t.Cleanup(RegisterObserver(func(_ context.Context, ev Event) {
		if physicalDir(ev.ProjectDir) == physicalDir(realDir) {
			count.Add(1)
			delivered = ev
		}
	}))

	d := newDebouncer(40 * time.Millisecond)
	d.NotifyWorktree(context.Background(), realDir, []string{filepath.Join(realDir, "sub/inside.go")}, SourceMutation)
	d.NotifyWorktree(context.Background(), aliasDir, []string{"other.go"}, SourceMutation)
	d.FlushForTest(t.Context())

	if count.Load() != 1 {
		t.Fatalf("deliveries = %d, want 1 coalesced event across symlink aliases", count.Load())
	}
	if !slices.Contains(delivered.Paths, "sub/inside.go") || !slices.Contains(delivered.Paths, "other.go") {
		t.Fatalf("delivered paths = %v, want normalized relative paths", delivered.Paths)
	}
	if !EpochCurrent(aliasDir, Epoch{BootID: delivered.EpochBootID, Value: delivered.WorktreeEpoch}) {
		t.Fatal("alias epoch disagreed with delivered physical epoch")
	}
	if !delivered.ContiguousAfter(startEpoch) {
		t.Fatalf("coalesced burst broke epoch contiguity: start=%v delivered=%+v", startEpoch, delivered)
	}
	if (Event{EpochBootID: startEpoch.BootID, FirstWorktreeEpoch: startEpoch.Value + 2, WorktreeEpoch: startEpoch.Value + 2}).ContiguousAfter(startEpoch) {
		t.Fatal("skipped epoch falsely reported contiguous")
	}

	privDir := filepath.Join(realDir, ".private-state")
	testutil.FailErr(t, "mkdir privDir", os.MkdirAll(privDir, 0o755))
	t.Cleanup(HoldPrivateTree(privDir))
	epochBeforePrivate := CurrentEpoch(realDir)
	d.NotifyWorktreeChanges(context.Background(), realDir, []WorktreeChange{{Path: ".private-state/internal", Kind: WorktreeChangeContent}}, SourceWatcher)
	d.FlushForTest(t.Context())
	if CurrentEpoch(realDir) != epochBeforePrivate {
		t.Fatal("private watcher path advanced public worktree epoch")
	}

	// Whole-root resync in a coalesced burst must survive subsequent path additions.
	d.NotifyWorktree(context.Background(), realDir, []string{"before.go"}, SourceMutation)
	d.NotifyWorktreeChanges(context.Background(), aliasDir, []WorktreeChange{{Path: ".", Kind: WorktreeChangeResync}}, SourceMutation)
	d.NotifyWorktree(context.Background(), realDir, []string{"after.go"}, SourceMutation)
	d.FlushForTest(t.Context())

	if !StructuralPaths(delivered).Full {
		t.Fatalf("coalesced resync lost full-tree classification: %+v", delivered)
	}
}

func TestNotifyNormalizesInRootAbsolutePathsAndKeepsEscapingPathsDistinct(t *testing.T) {
	root := t.TempDir()
	var got Event
	t.Cleanup(RegisterObserver(func(_ context.Context, ev Event) {
		if ev.ProjectDir == root {
			got = ev
		}
	}))

	Notify(t.Context(), Event{
		ProjectDir: root,
		Kind:       WorktreeChanged,
		Paths:      []string{filepath.Join(root, "nested", ".env"), "../escape"},
		Source:     SourceMutation,
	})
	if !slices.Contains(got.Paths, "nested/.env") {
		t.Fatalf("in-root absolute path was not normalized: %v", got.Paths)
	}
	if !slices.Contains(got.Paths, "../escape") {
		t.Fatalf("escaping path should remain distinct for downstream guards: %v", got.Paths)
	}
}
