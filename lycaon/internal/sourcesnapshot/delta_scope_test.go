package sourcesnapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A nested ignore file decides only the directory it sits in, so its change
// re-reads that directory and nothing else.
func TestNestedIgnoreFileChangeRereadsOnlyItsDirectory(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	for _, name := range []string{"pkg/keep.go", "pkg/gen/out.go", "other/a.go", "other/b.go", "other/c.go"} {
		writeSource(t, root, name, "package x // "+name+"\n")
	}
	request := Request{Roots: []Root{{Path: root}}}
	_, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish first generation", err)

	surveys, reads := surveyCount(t, store), captureCount(t, store)
	writeSource(t, root, "pkg/.gitignore", "gen/\n")
	notifyChange(root, "pkg/.gitignore")
	second, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish after a nested ignore change", err)
	if got := surveys(); got != 0 {
		t.Fatalf("root surveys = %d, want 0: only pkg is governed by the change", got)
	}
	// pkg/keep.go and the new pkg/.gitignore; nothing under other/.
	if got := reads(); got > 2 {
		t.Fatalf("files re-read = %d, want only the governed directory", got)
	}
	paths := entryPaths(t, store, second)
	if _, ok := paths["pkg/gen/out.go"]; ok {
		t.Fatal("pkg/gen/out.go still admitted after its directory's ignore file excluded it")
	}
	for _, want := range []string{"pkg/keep.go", "other/a.go", "other/b.go", "other/c.go"} {
		if _, ok := paths[want]; !ok {
			t.Fatalf("%s lost by a change to another directory's ignore file: %v", want, paths)
		}
	}

	testutil.FailErr(t, "remove nested ignore file", os.Remove(filepath.Join(root, "pkg", ".gitignore")))
	notifyChange(root, "pkg/.gitignore")
	third, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish after removing the ignore file", err)
	if _, ok := entryPaths(t, store, third)["pkg/gen/out.go"]; !ok {
		t.Fatal("removing the ignore file did not re-admit what it excluded")
	}
	if got := surveys(); got != 0 {
		t.Fatalf("root surveys = %d after removing a nested ignore file, want 0", got)
	}
}

// A burst wider than the tracker holds folds into the directories that
// contain it and still publishes as a delta.
func TestWideBurstFoldsIntoItsDirectories(t *testing.T) {
	store := openSnapshotStore(t)
	store.deltas.pathLimit = 8
	root := t.TempDir()
	coverRoot(t, root)
	writeSource(t, root, "keep.go", "package keep\n")
	changed := make([]string, 0, 20)
	for i := range 20 {
		name := fmt.Sprintf("batch/out/file-%02d.go", i)
		writeSource(t, root, name, "package out // 0\n")
		changed = append(changed, name)
	}
	request := Request{Roots: []Root{{Path: root}}}
	_, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish first generation", err)

	surveys := surveyCount(t, store)
	for _, name := range changed {
		writeSource(t, root, name, "package out // 1\n")
	}
	writeSource(t, root, "batch/out/added.go", "package out\n")
	notifyChange(root, append(changed, "batch/out/added.go")...)
	second, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish the folded burst", err)
	if got := surveys(); got != 0 {
		t.Fatalf("root surveys = %d, want 0: the burst folds into batch/out", got)
	}
	if second.FileCount != 22 {
		t.Fatalf("files = %d, want every changed and added file", second.FileCount)
	}
	entry, ok, err := store.Lookup(t.Context(), second.ID, root, "batch/out/file-07.go")
	testutil.FailErr(t, "look up a folded change", err)
	if !ok || entry.Size != int64(len("package out // 1\n")) {
		t.Fatalf("folded change not captured: %+v", entry)
	}
}

// A burst that only the root itself can hold sends the publication back to
// a survey.
func TestBurstFoldingToTheRootSurveys(t *testing.T) {
	store := openSnapshotStore(t)
	store.deltas.pathLimit = 2
	root := t.TempDir()
	coverRoot(t, root)
	names := []string{"a.go", "b.go", "c.go", "d.go"}
	for _, name := range names {
		writeSource(t, root, name, "package x\n")
	}
	request := Request{Roots: []Root{{Path: root}}}
	_, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish first generation", err)

	surveys := surveyCount(t, store)
	for _, name := range names {
		writeSource(t, root, name, "package x // changed\n")
	}
	notifyChange(root, names...)
	_, err = store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish after a root-wide burst", err)
	if got := surveys(); got != 1 {
		t.Fatalf("root surveys = %d, want 1", got)
	}
}

func TestFoldKeepsTheLatestSequencePerDirectory(t *testing.T) {
	tracker := newDeltaTracker()
	tracker.pathLimit = 2
	root := t.TempDir()
	for _, rel := range []string{"a/x/1.go", "a/x/2.go", "a/y/3.go", "b/4.go"} {
		tracker.observe(context.Background(), repochange.Event{
			ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: []string{filepath.Join(root, rel)},
		})
	}
	d := tracker.roots[filepath.Clean(root)]
	if d.overflow || len(d.paths) != 2 {
		t.Fatalf("folded paths = %v overflow=%v", d.paths, d.overflow)
	}
	// a/x, a/y, and b/4.go share the deepest level, so all three fold.
	if d.paths["a"] != 3 || d.paths["b"] != 4 {
		t.Fatalf("folded sequences = %v, want each directory's latest", d.paths)
	}
}

func TestOverflowDropsPathsAndPreservesLaterInvalidation(t *testing.T) {
	tracker := newDeltaTracker()
	tracker.pathLimit = 2
	root := t.TempDir()
	observe := func(paths ...string) {
		tracker.observe(t.Context(), repochange.Event{ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: paths})
	}
	observe("a.go", "b.go", "c.go")
	before := tracker.sequence()
	observe("d.go", "e.go")
	d := tracker.roots[root]
	if !d.overflow || len(d.paths) != 0 || d.overflowAt <= before {
		t.Fatalf("overflow retained paths or lost a later change: %+v", d)
	}
	tracker.coveredBy(root, "old", "scope", before, true)
	if !d.overflow {
		t.Fatal("an earlier publication cleared a later overflow")
	}
	plan := tracker.pending(root, deltaPlan{upTo: before})
	if !plan.overflow || d.overflow || len(d.paths) != 0 {
		t.Fatalf("pending plan did not consume overflow: %+v, %+v", plan, d)
	}
	observe("fresh.go")
	if len(d.paths) != 1 {
		t.Fatalf("tracking did not resume after overflow: %+v", d)
	}
}
