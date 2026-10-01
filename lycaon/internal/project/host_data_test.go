package project_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHostDataDirLayout(t *testing.T) {
	base := t.TempDir()
	got := project.HostDataDir(base, "proj-1")
	want := filepath.Join(base, "projects", "proj-1")
	if got != want {
		t.Fatalf("HostDataDir = %q want %q", got, want)
	}
	if project.HostDataDir(base, "") != "" {
		t.Fatal("empty project id should yield empty dir")
	}
}

func TestEnsureAndRemoveHostDataDir(t *testing.T) {
	base := t.TempDir()
	dir, err := project.EnsureHostDataDir(base, "proj-wipe")
	testutil.FailErr(t, "ensure", err)
	marker := filepath.Join(dir, "tool-output", "a.txt")
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(marker), 0o700))
	testutil.FailErr(t, "write", os.WriteFile(marker, []byte("x"), 0o600))
	testutil.FailErr(t, "remove", project.RemoveHostDataDir(base, "proj-wipe"))
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("host data dir still present: %v", err)
	}
}

func TestReconcileHostStorageKeepsOnlyLiveProjectTrees(t *testing.T) {
	base := t.TempDir()
	for _, rel := range []string{
		"projects/keep", "projects/orphan", "drafts/keep", "drafts/orphan",
	} {
		testutil.FailErr(t, "seed "+rel, os.MkdirAll(filepath.Join(base, rel), 0o700))
	}
	removed, err := project.ReconcileHostStorage(base, map[string]struct{}{"keep": {}})
	testutil.FailErr(t, "reconcile", err)
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	for _, rel := range []string{"projects/keep", "drafts/keep"} {
		if _, err := os.Stat(filepath.Join(base, rel)); err != nil {
			t.Fatalf("live tree %s removed: %v", rel, err)
		}
	}
	for _, rel := range []string{"projects/orphan", "drafts/orphan"} {
		if _, err := os.Stat(filepath.Join(base, rel)); !os.IsNotExist(err) {
			t.Fatalf("orphan tree %s survived: %v", rel, err)
		}
	}
}
