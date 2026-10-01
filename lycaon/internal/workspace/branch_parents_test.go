package workspace_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
)

func TestEnsureParentsRefusesSymlinkTraversal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture requires unix permissions")
	}
	branch := t.TempDir()
	outside := t.TempDir()
	testutil.FailErr(t, "create symlink", os.Symlink(outside, filepath.Join(branch, "link")))

	if err := workspace.EnsureParents(branch, "link/nested/file.txt"); err == nil {
		t.Fatal("EnsureParents followed a symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "nested")); !os.IsNotExist(err) {
		t.Fatalf("outside directory exists: %v", err)
	}
}

func TestEnsureParentsRejectsInvalidAuthority(t *testing.T) {
	if err := workspace.EnsureParents("relative", "nested/file.txt"); err == nil {
		t.Fatal("EnsureParents accepted a relative root")
	}
	if err := workspace.EnsureParents(t.TempDir(), "/outside/file.txt"); err == nil {
		t.Fatal("EnsureParents accepted an absolute branch path")
	}
	if err := workspace.EnsureParents(t.TempDir(), "../outside/file.txt"); err == nil {
		t.Fatal("EnsureParents accepted parent traversal")
	}
}
