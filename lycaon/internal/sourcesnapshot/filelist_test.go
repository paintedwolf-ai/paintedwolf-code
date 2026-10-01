package sourcesnapshot

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAdmittedFilesAppliesTheFloorAndIgnoreFiles(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "main.go", "package main\n")
	writeSource(t, root, "internal/deep/util.go", "package deep\n")
	writeSource(t, root, ".hidden/config.yaml", "a: 1\n")
	writeSource(t, root, "node_modules/dep/index.js", "module.exports = 1\n")
	writeSource(t, root, ".gitignore", "*.log\n")
	writeSource(t, root, "internal/debug.log", "")

	store := openSnapshotStore(t)
	scope := store.scopes.Capture(t.Context(), root)
	refs, boundaries := admittedPaths(t, root, scope, ".")

	paths := refPaths(refs)
	slices.Sort(paths)
	want := []string{".gitignore", ".hidden/config.yaml", "internal/deep/util.go", "main.go"}
	if !slices.Equal(paths, want) {
		t.Fatalf("admitted set = %v, want %v", paths, want)
	}
	for _, ref := range refs {
		if _, err := os.Stat(ref.Abs); err != nil {
			t.Fatalf("admitted file is not readable: %v", err)
		}
		info, err := ref.stat()
		testutil.FailErr(t, "stat from listing", err)
		if !info.Mode().IsRegular() {
			t.Fatalf("listing stat for %s = %v", ref.Path, info.Mode())
		}
	}
	if refs[0].Abs != filepath.Join(root, filepath.FromSlash(refs[0].Path)) {
		t.Fatalf("admitted abs path = %q", refs[0].Abs)
	}
	if len(boundaries) != 1 || boundaries[0].Path != "node_modules" || boundaries[0].Budgeted() {
		t.Fatalf("boundaries = %+v", boundaries)
	}
}

func TestAdmittedUnderSurveysASubtreeWithRootRelativePaths(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "a/keep.go", "")
	writeSource(t, root, "a/node_modules/x.js", "")
	writeSource(t, root, "b/other.go", "")

	store := openSnapshotStore(t)
	scope := store.scopes.Capture(t.Context(), root)
	refs, boundaries := admittedPaths(t, root, scope, "a")
	paths := refPaths(refs)
	if !slices.Equal(paths, []string{"a/keep.go"}) {
		t.Fatalf("subtree admitted = %v", paths)
	}
	if len(boundaries) != 1 || boundaries[0].Path != "a/node_modules" {
		t.Fatalf("subtree boundaries = %+v", boundaries)
	}
}

func TestPublishReadsNoTreeWhenNothingChanged(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	for _, name := range []string{"a.go", "b.go"} {
		writeSource(t, root, name, "package a // "+name+"\n")
	}
	request := Request{Roots: []Root{{Path: root}}}
	first, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish first generation", err)

	captured := captureCount(t, store)
	repochange.Advance(root)
	second, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish second generation", err)
	if second.ID != first.ID {
		t.Fatalf("identity moved without a content change: %q vs %q", second.ID, first.ID)
	}
	if got := captured(); got != 0 {
		t.Fatalf("files re-read with nothing changed = %d, want 0", got)
	}
}
