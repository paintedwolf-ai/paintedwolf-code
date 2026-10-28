package gitrepo_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEnsureRecoveryExcludedAppendsOnceAndKeepsMode(t *testing.T) {
	root := plainRepo(t)
	exclude := filepath.Join(root, ".git", "info", "exclude")
	testutil.FailErr(t, "ensure first", gitrepo.EnsureRecoveryExcluded(root))
	data, err := os.ReadFile(exclude)
	testutil.FailErr(t, "read created exclude", err)
	if string(data) != ".paintedwolf/recovery/\n" {
		t.Fatalf("created exclude = %q", data)
	}

	testutil.FailErr(t, "seed existing exclude", os.WriteFile(exclude, []byte("*.log"), 0o600))
	testutil.FailErr(t, "narrow exclude mode", os.Chmod(exclude, 0o600))
	testutil.FailErr(t, "ensure append", gitrepo.EnsureRecoveryExcluded(root))
	testutil.FailErr(t, "ensure repeat", gitrepo.EnsureRecoveryExcluded(root))
	data, err = os.ReadFile(exclude)
	testutil.FailErr(t, "read appended exclude", err)
	if string(data) != "*.log\n.paintedwolf/recovery/\n" {
		t.Fatalf("appended exclude = %q", data)
	}
	info, err := os.Stat(exclude)
	testutil.FailErr(t, "stat exclude", err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("exclude mode = %v, want preserved 0600", info.Mode().Perm())
	}
}

func TestEnsureRecoveryExcludedOutsideRepositoryWritesNothing(t *testing.T) {
	dir := canonicalTempDir(t)
	testutil.FailErr(t, "ensure outside repository", gitrepo.EnsureRecoveryExcluded(dir))
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Fatalf("outside repository created .git: %v", err)
	}
}
