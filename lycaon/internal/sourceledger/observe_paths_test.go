package sourceledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/gitstate"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func trackRootFile(t *testing.T, store *Store, ctx context.Context, root, rel, content string) {
	t.Helper()
	writeRootFile(t, root, rel, content)
	sum := sha256.Sum256([]byte(content))
	_, err := store.TrackFile(ctx, TrackInput{
		ProjectID: "p1", RootID: "r1", Path: rel, BranchID: sourcebranch.Trunk, EntryKind: EntryKindFile,
		SHA256: hex.EncodeToString(sum[:]), Content: []byte(content), Size: int64(len(content)),
	})
	testutil.FailErr(t, "track "+rel, err)
}

func trunkEffects(t *testing.T, store *Store, ctx context.Context) map[string]Effect {
	t.Helper()
	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{}, 50, 0, CommitLens{})
	testutil.FailErr(t, "query walk", err)
	return effectsByPath(walk)
}

// A named outside edit to a tracked file moves history without a pass.
func TestObservePathsRecordsOutsideEditToTrackedFile(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	trackRootFile(t, store, ctx, root, "docs/a.md", "before\n")
	writeRootFile(t, root, "docs/a.md", "after\n")

	recorded, err := store.Inventory.ObservePaths(ctx, "p1", onDiskRoots(root), []PathRef{{RootID: "r1", Path: "docs/a.md"}})
	testutil.FailErr(t, "observe paths", err)
	if recorded != 1 {
		t.Fatalf("recorded = %d, want 1", recorded)
	}
	effect, ok := trunkEffects(t, store, ctx)["docs/a.md"]
	if !ok {
		t.Fatal("no effect recorded for docs/a.md")
	}
	if effect.Op != api.SourceChangeOpWrite || effect.Origin != api.SourceChangeOriginExternal ||
		effect.Cause != CauseFilesystemReconcile || effect.CaptureQuality != CaptureReconciled {
		t.Fatalf("effect = %+v", effect)
	}

	// The same bytes seen again record nothing.
	recorded, err = store.Inventory.ObservePaths(ctx, "p1", onDiskRoots(root), []PathRef{{RootID: "r1", Path: "docs/a.md"}})
	testutil.FailErr(t, "observe unchanged", err)
	if recorded != 0 {
		t.Fatalf("unchanged file recorded %d effects", recorded)
	}
}

func TestObservePathsDoesNotReuseMetadataAfterReportedWrite(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	trackRootFile(t, store, ctx, root, "same.txt", "before\n")
	file := filepath.Join(root, "same.txt")
	info, err := os.Stat(file)
	testutil.FailErr(t, "stat before", err)
	_, _, err = store.Snapshots.Identify(ctx, root, "same.txt")
	testutil.FailErr(t, "warm observation", err)
	writeRootFile(t, root, "same.txt", "edited\n")
	testutil.FailErr(t, "preserve external timestamp", os.Chtimes(file, info.ModTime(), info.ModTime()))
	recorded, err := store.Inventory.ObservePaths(ctx, "p1", onDiskRoots(root), []PathRef{{RootID: "r1", Path: "same.txt"}})
	testutil.FailErr(t, "observe changed bytes", err)
	if recorded != 1 {
		t.Fatalf("same-metadata outside edit recorded %d effects, want 1", recorded)
	}
}

func TestObservePathsRecordsOutsideDeleteOfTrackedFile(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	trackRootFile(t, store, ctx, root, "gone.txt", "bytes\n")
	testutil.FailErr(t, "remove", os.Remove(filepath.Join(root, "gone.txt")))

	recorded, err := store.Inventory.ObservePaths(ctx, "p1", onDiskRoots(root), []PathRef{{RootID: "r1", Path: "gone.txt"}})
	testutil.FailErr(t, "observe delete", err)
	if recorded != 1 {
		t.Fatalf("recorded = %d, want 1", recorded)
	}
	if effect := trunkEffects(t, store, ctx)["gone.txt"]; effect.Op != api.SourceChangeOpDelete {
		t.Fatalf("effect = %+v, want delete", effect)
	}
}

func TestObservePathsDirectoryEventRecordsTrackedDescendants(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "replacement", true: "removal"}[deleted], func(t *testing.T) {
			store, ctx, root := openLedgerOnDisk(t)
			trackRootFile(t, store, ctx, root, "src/a.txt", "before\n")
			trackRootFile(t, store, ctx, root, "src/deep/b.txt", "before\n")
			_, err := store.TrackFile(ctx, TrackInput{
				ProjectID: "p1", RootID: "r1", Path: "src", BranchID: sourcebranch.Trunk, EntryKind: EntryKindDirectory,
			})
			testutil.FailErr(t, "track directory", err)
			_, err = store.TrackFile(ctx, TrackInput{
				ProjectID: "p1", RootID: "r1", Path: "src/deep", BranchID: sourcebranch.Trunk, EntryKind: EntryKindDirectory,
			})
			testutil.FailErr(t, "track nested directory", err)
			trackRootFile(t, store, ctx, root, "src-other/c.txt", "before\n")
			writeRootFile(t, root, "src-other/c.txt", "not selected\n")
			wantOp := api.SourceChangeOpWrite
			if deleted {
				testutil.FailErr(t, "remove directory", os.RemoveAll(filepath.Join(root, "src")))
				wantOp = api.SourceChangeOpDelete
			} else {
				writeRootFile(t, root, "src/a.txt", "after\n")
				writeRootFile(t, root, "src/deep/b.txt", "after\n")
			}
			recorded, err := store.Inventory.ObservePaths(ctx, "p1", onDiskRoots(root), []PathRef{
				{RootID: "r1", Path: "src"}, {RootID: "r1", Path: "src/a.txt"},
			})
			testutil.FailErr(t, "observe directory and overlapping file", err)
			if recorded != 2 {
				t.Fatalf("recorded %d changes, want 2", recorded)
			}
			effects := trunkEffects(t, store, ctx)
			for _, rel := range []string{"src/a.txt", "src/deep/b.txt"} {
				if effect := effects[rel]; effect.Op != wantOp || effect.Origin != api.SourceChangeOriginExternal {
					t.Fatalf("effect for %s = %+v", rel, effect)
				}
			}
			if _, exists := effects["src-other/c.txt"]; exists {
				t.Fatal("directory selection included a sibling")
			}
		})
	}
}

// One call is one observation batch; every path it records shares the batch.
func TestObservePathsStampsOneBatchPerCall(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	trackRootFile(t, store, ctx, root, "a.txt", "a\n")
	trackRootFile(t, store, ctx, root, "b.txt", "b\n")
	writeRootFile(t, root, "a.txt", "a2\n")
	writeRootFile(t, root, "b.txt", "b2\n")

	recorded, err := store.Inventory.ObservePaths(ctx, "p1", onDiskRoots(root), []PathRef{
		{RootID: "r1", Path: "a.txt"}, {RootID: "r1", Path: "b.txt"},
	})
	testutil.FailErr(t, "observe batch", err)
	if recorded != 2 {
		t.Fatalf("recorded = %d, want 2", recorded)
	}
	effects := trunkEffects(t, store, ctx)
	if effects["a.txt"].BatchID == "" || effects["a.txt"].BatchID != effects["b.txt"].BatchID {
		t.Fatalf("batch ids = %q, %q", effects["a.txt"].BatchID, effects["b.txt"].BatchID)
	}

	writeRootFile(t, root, "a.txt", "a3\n")
	_, err = store.Inventory.ObservePaths(ctx, "p1", onDiskRoots(root), []PathRef{{RootID: "r1", Path: "a.txt"}})
	testutil.FailErr(t, "observe again", err)
	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{}, 50, 0, CommitLens{})
	testutil.FailErr(t, "query walk", err)
	batches := make(map[string]struct{})
	for _, file := range walk.Files {
		for _, effect := range file.Effects {
			batches[effect.BatchID] = struct{}{}
		}
	}
	if len(batches) != 2 {
		t.Fatalf("batches = %v, want one per call", batches)
	}
}

// Git is observed before the bytes, so a checkout the watcher named by path
// records its files against the movement rather than as anonymous drift.
func TestObservePathsNamesTheGitMovementItObserves(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	reader := &fakeGitReader{states: map[string]gitstate.State{
		root: {Repo: gitstate.RepoPresent, HeadCommit: "aaa", HeadRef: "main"},
	}}
	store.Git.SetGitReader(reader)
	_, err := store.Git.ObserveGitState(ctx, "p1", onDiskRoots(root))
	testutil.FailErr(t, "seed git state", err)
	trackRootFile(t, store, ctx, root, "swapped.txt", "main\n")

	writeRootFile(t, root, "swapped.txt", "work\n")
	reader.states[root] = gitstate.State{Repo: gitstate.RepoPresent, HeadCommit: "bbb", HeadRef: "work"}
	reader.logs = map[string][]gitstate.RefLogEntry{root: {
		{Commit: "bbb", Subject: "checkout: moving from main to work"},
		{Commit: "aaa", Subject: "commit: earlier"},
	}}
	recorded, err := store.Inventory.ObservePaths(ctx, "p1", onDiskRoots(root), []PathRef{{RootID: "r1", Path: "swapped.txt"}})
	testutil.FailErr(t, "observe checkout paths", err)
	if recorded != 1 {
		t.Fatalf("recorded = %d, want 1", recorded)
	}
	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{}, 10, 0, CommitLens{})
	testutil.FailErr(t, "query walk", err)
	effect := effectsByPath(walk)["swapped.txt"]
	if effect.GitTransitionID == "" {
		t.Fatalf("effect names no movement: %+v", effect)
	}
	if transition, ok := walkGitChangeByID(walk, effect.GitTransitionID); !ok || transition.ToRef != "work" {
		t.Fatalf("walk git changes = %+v", walk.GitChanges)
	}
}

// Untracked files stay outside history, as they do in a pass with no open
// command window; a path under an unknown root is ignored.
func TestObservePathsSkipsUntrackedAndUnknownRoots(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	writeRootFile(t, root, "scratch.txt", "untracked\n")
	trackRootFile(t, store, ctx, root, "known.txt", "known\n")
	writeRootFile(t, root, "known.txt", "changed\n")

	recorded, err := store.Inventory.ObservePaths(ctx, "p1", onDiskRoots(root), []PathRef{
		{RootID: "r1", Path: "scratch.txt"},
		{RootID: "r-elsewhere", Path: "known.txt"},
	})
	testutil.FailErr(t, "observe", err)
	if recorded != 0 {
		t.Fatalf("recorded = %d, want 0", recorded)
	}
	if _, ok := trunkEffects(t, store, ctx)["scratch.txt"]; ok {
		t.Fatal("an untracked file entered history without a window")
	}
}

func TestWorktreeObservationNeverMovesBaseHeads(t *testing.T) {
	store, ctx, base := openLedgerOnDisk(t)
	checkout := t.TempDir()
	branch := sourcebranch.ForWorktree("checkout-id")
	roots := []RootSpec{{ID: "r1", Path: checkout, BranchID: branch}}
	trackRootFile(t, store, ctx, base, "a.txt", "base\n")
	writeRootFile(t, checkout, "a.txt", "checkout\n")
	_, err := store.TrackFile(ctx, TrackInput{ProjectID: "p1", BranchID: branch, RootID: "r1", Path: "a.txt", EntryKind: EntryKindFile, Content: []byte("checkout\n"), SHA256: sourceblob.ContentSHA([]byte("checkout\n")), Size: 9})
	testutil.FailErr(t, "track checkout", err)
	reader := &fakeGitReader{states: map[string]gitstate.State{base: {Repo: gitstate.RepoPresent, HeadCommit: "base", HeadRef: "main"}, checkout: {Repo: gitstate.RepoPresent, HeadCommit: "checkout", HeadRef: "feature"}}}
	store.Git.SetGitReader(reader)
	for _, selection := range [][]RootSpec{onDiskRoots(base), roots, onDiskRoots(base), roots} {
		changes, err := store.Git.ObserveGitState(ctx, "p1", selection)
		testutil.FailErr(t, "observe checkout git state", err)
		if len(changes) != 0 {
			t.Fatal("different checkouts fabricated a Git transition")
		}
	}
	testutil.FailErr(t, "initialize base inventory", store.Inventory.EnsureInventory(ctx, InventoryRequest{ProjectID: "p1", RootsGeneration: 1, Roots: onDiskRoots(base)}))
	writeRootFile(t, checkout, "a.txt", "outside checkout\n")
	count, err := store.Inventory.ObservePaths(ctx, "p1", roots, []PathRef{{RootID: "r1", Path: "a.txt"}})
	testutil.FailErr(t, "observe checkout outside edit", err)
	if count != 1 {
		t.Fatalf("checkout observations=%d", count)
	}
	testutil.FailErr(t, "inventory checkout", store.Inventory.EnsureInventory(ctx, InventoryRequest{ProjectID: "p1", RootsGeneration: 1, Roots: roots}))
	for _, selected := range []sourcebranch.ID{sourcebranch.Trunk, branch} {
		head, err := store.History.ResolveHead(ctx, "p1", selected, "r1", "a.txt")
		testutil.FailErr(t, "resolve checkout head", err)
		want := "base\n"
		if selected == branch {
			want = "outside checkout\n"
		}
		if head.SHA256 != sourceblob.ContentSHA([]byte(want)) {
			t.Fatalf("wrong head for %q: %+v", selected, head)
		}
		state, err := store.Inventory.InventoryState(ctx, "p1", selected, 1)
		testutil.FailErr(t, "read checkout inventory", err)
		if !state.Complete {
			t.Fatalf("checkout inventory incomplete: %+v", state)
		}
	}
}
