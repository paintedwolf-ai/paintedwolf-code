//go:build linux

package fseffect

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

// withoutNoReplace stands in a filesystem, such as NFS, that refuses
// RENAME_NOREPLACE.
func withoutNoReplace(t *testing.T) {
	t.Helper()
	previous := renameat2
	renameat2 = func(int, string, int, string, uint) error { return unix.EINVAL }
	t.Cleanup(func() { renameat2 = previous })
}

func TestRenameNoReplaceFallsBackWithoutKernelSupport(t *testing.T) {
	withoutNoReplace(t)
	root := t.TempDir()
	testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(root, "file"), []byte("saved"), 0o600))
	testutil.FailErr(t, "publish file", RenameNoReplace(root, "file", "published"))
	if _, err := os.Lstat(filepath.Join(root, "file")); !os.IsNotExist(err) {
		t.Fatalf("source remained after publication: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(root, "published")); err != nil || string(body) != "saved" {
		t.Fatalf("published file = %q, %v", body, err)
	}
	testutil.FailErr(t, "seed link", os.Symlink("published", filepath.Join(root, "link")))
	testutil.FailErr(t, "publish link", RenameNoReplace(root, "link", "moved-link"))
	if target, err := os.Readlink(filepath.Join(root, "moved-link")); err != nil || target != "published" {
		t.Fatalf("moved link = %q, %v", target, err)
	}
	testutil.FailErr(t, "seed folder", os.Mkdir(filepath.Join(root, "folder"), 0o700))
	testutil.FailErr(t, "seed folder content", os.WriteFile(filepath.Join(root, "folder", "inside"), nil, 0o600))
	testutil.FailErr(t, "publish folder", RenameNoReplace(root, "folder", "moved-folder"))
	if _, err := os.Stat(filepath.Join(root, "moved-folder", "inside")); err != nil {
		t.Fatalf("published folder content: %v", err)
	}
}

func TestRenameNoReplaceFallbackPreservesOccupants(t *testing.T) {
	withoutNoReplace(t)
	root := t.TempDir()
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(root, "source"), []byte("saved"), 0o600))
	testutil.FailErr(t, "seed occupant", os.WriteFile(filepath.Join(root, "dest"), []byte("new"), 0o600))
	if err := RenameNoReplace(root, "source", "dest"); !os.IsExist(err) {
		t.Fatalf("file over occupant: %v", err)
	}
	testutil.FailErr(t, "seed folder", os.Mkdir(filepath.Join(root, "folder"), 0o700))
	testutil.FailErr(t, "seed folder occupant", os.Mkdir(filepath.Join(root, "taken"), 0o700))
	if err := RenameNoReplace(root, "folder", "taken"); !os.IsExist(err) {
		t.Fatalf("folder over occupant: %v", err)
	}
	for name, want := range map[string]string{"source": "saved", "dest": "new"} {
		body, err := os.ReadFile(filepath.Join(root, name))
		testutil.FailErr(t, "read "+name, err)
		if string(body) != want {
			t.Fatalf("%s = %q, want %q", name, body, want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "folder")); err != nil {
		t.Fatalf("refused folder lost: %v", err)
	}
}
