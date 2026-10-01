package sourcecatalog

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestIncrementalCatalogMatchesFullSurvey(t *testing.T) {
	root := Root{ID: "root", Path: t.TempDir()}
	write := func(rel, text string) {
		t.Helper()
		abs := filepath.Join(root.Path, rel)
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "write", os.WriteFile(abs, []byte(text), 0o644))
	}
	write(".bin/cache/a", "cache")
	write("node_modules/a/index.js", "dependency")
	write("target/build/a", "artifact")
	write("src/old.go", "old")
	write("obsolete/deep/old.go", "old")
	write("root.txt", "old")
	before, err := buildSnapshot(t.Context(), []Root{root}, walkPolicy{})
	testutil.FailErr(t, "initial survey", err)
	write("root.txt", "changed")
	write("src/new/deep.go", "new")
	testutil.FailErr(t, "remove", os.Remove(filepath.Join(root.Path, "src/old.go")))
	testutil.FailErr(t, "remove obsolete file", os.Remove(filepath.Join(root.Path, "obsolete/deep/old.go")))
	testutil.FailErr(t, "remove obsolete directory", os.Remove(filepath.Join(root.Path, "obsolete/deep")))
	testutil.FailErr(t, "remove obsolete root", os.Remove(filepath.Join(root.Path, "obsolete")))
	write(".bin/cache/a", "changed cache")
	after, err := reconcilePaths(t.Context(), root, before, []string{"root.txt", "src/old.go", "src/new/deep.go", ".bin/cache/a", "obsolete/deep/old.go", "obsolete/other.go"}, walkPolicy{})
	testutil.FailErr(t, "incremental survey", err)
	full, err := buildSnapshot(t.Context(), []Root{root}, walkPolicy{})
	testutil.FailErr(t, "full survey", err)
	if len(after.Entries) != len(full.Entries) {
		t.Fatalf("incremental entries=%d full=%d", len(after.Entries), len(full.Entries))
	}
	for _, expected := range full.Entries {
		actual, ok := after.Entry(root.ID, expected.Path)
		if !ok || !reflect.DeepEqual(actual, expected) {
			t.Fatalf("entry %s: got %+v, want %+v", expected.Path, actual, expected)
		}
	}
}

func TestIncrementalCatalogDoesNotTraverseReplacedSymlink(t *testing.T) {
	root := Root{ID: "root", Path: t.TempDir()}
	testutil.FailErr(t, "mkdir", os.Mkdir(filepath.Join(root.Path, "dir"), 0o755))
	before, err := buildSnapshot(t.Context(), []Root{root}, walkPolicy{})
	testutil.FailErr(t, "survey", err)
	testutil.FailErr(t, "remove empty dir", os.Remove(filepath.Join(root.Path, "dir")))
	outside := t.TempDir()
	testutil.FailErr(t, "outside file", os.WriteFile(filepath.Join(outside, "file"), []byte("outside"), 0o644))
	testutil.FailErr(t, "symlink", os.Symlink(outside, filepath.Join(root.Path, "dir")))
	after, err := reconcilePaths(t.Context(), root, before, []string{"dir/file"}, walkPolicy{})
	testutil.FailErr(t, "reconcile replaced parent", err)
	if _, ok := after.Entry(root.ID, "dir/file"); ok {
		t.Fatal("traversed symlink")
	}
	if entry, ok := after.Entry(root.ID, "dir"); !ok || !entry.IsSymlink {
		t.Fatalf("symlink = %+v", entry)
	}
}
