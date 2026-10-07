package sourcesnapshot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestMaterializeIsolatesSelectedSnapshotBytes(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	writeSource(t, root, "a.go", "package a\n")
	writeSource(t, root, "b.go", "package b\n")
	snapshot, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}})
	testutil.FailErr(t, "capture", err)
	dir, err := store.Materialize(t.Context(), snapshot.ID, root, []string{"a.go"})
	testutil.FailErr(t, "materialize", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	writeSource(t, root, "a.go", "changed live content")
	raw, err := os.ReadFile(filepath.Join(dir, "a.go"))
	testutil.FailErr(t, "read isolated input", err)
	if string(raw) != "package a\n" {
		t.Fatalf("snapshot changed: %q", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.go")); !os.IsNotExist(err) {
		t.Fatalf("delta widened: %v", err)
	}
	_, err = store.Materialize(t.Context(), snapshot.ID, root, []string{"a.go"})
	if !errors.Is(err, ErrContentUnavailable) {
		t.Fatalf("unavailable old bytes substituted: %v", err)
	}
	if _, err := store.Materialize(t.Context(), snapshot.ID, t.TempDir(), nil); err == nil {
		t.Fatal("foreign root accepted")
	}
	if _, err := store.Materialize(t.Context(), snapshot.ID, root, []string{"missing.go"}); err == nil {
		t.Fatal("missing target accepted")
	}
}

func TestMaterializeRestoresCapturedGitBytesAndSeparatesRoots(t *testing.T) {
	store := openSnapshotStore(t)
	root, other := t.TempDir(), t.TempDir()
	writeSource(t, root, "same.go", "package original\n")
	writeSource(t, other, "same.go", "package other\n")
	gittest.InitCommit(t, root, "captured")
	snapshot, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}, {Path: other}}})
	testutil.FailErr(t, "capture both roots", err)
	writeSource(t, root, "same.go", "package changed!\n")
	dir, err := store.Materialize(t.Context(), snapshot.ID, root, nil)
	testutil.FailErr(t, "materialize historical root", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	raw, err := os.ReadFile(filepath.Join(dir, "same.go"))
	testutil.FailErr(t, "read captured body", err)
	if string(raw) != "package original\n" {
		t.Fatalf("captured root substituted: %q", raw)
	}
}
