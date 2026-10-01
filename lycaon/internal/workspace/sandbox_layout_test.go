package workspace_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
)

func TestCreateWorkerWorkspaceFromSourcesMultiRoot(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "a")
	secondary := filepath.Join(base, "b")
	for _, dir := range []string{primary, secondary} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(dir, 0o755))
		name := filepath.Base(dir) + ".txt"
		testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644))
	}
	roots := []projectroot.RootRef{
		{ID: "p", Label: "a", Path: primary, IsPrimary: true},
		{ID: "s", Label: "b", Path: secondary, IsPrimary: false},
	}
	mgr := workspace.NewManager(filepath.Join(base, "branches"), filepath.Join(base, "seeds"))
	binding, layout, err := mgr.CreateWorkerWorkspaceFromSources(context.Background(), roots, roots, "p", "job-mr")
	testutil.FailErr(t, "CreateWorkerWorkspaceFromSources", err)
	primaryDir, err := projectroot.BranchDirForID(layout.Roots[0].ID)
	testutil.FailErr(t, "primary branch dir", err)
	secondaryDir, err := projectroot.BranchDirForID(layout.Roots[1].ID)
	testutil.FailErr(t, "secondary branch dir", err)
	if _, err := os.Stat(filepath.Join(binding.Root, primaryDir)); err != nil {
		t.Fatalf("primary branch directory missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(binding.Root, secondaryDir)); err != nil {
		t.Fatalf("secondary branch directory missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(binding.Root, primaryDir, "a.txt")); err != nil {
		t.Fatalf("primary snapshot missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(binding.Root, secondaryDir, "b.txt")); err != nil {
		t.Fatalf("secondary snapshot missing: %v", err)
	}
	meta, err := workspace.LoadJobMeta(enginepaths.MetaDirForBranchRoot(binding.Root))
	testutil.FailErr(t, "LoadJobMeta", err)
	if !meta.SnapshotComplete {
		t.Fatal("claimed branch must be a complete immutable snapshot")
	}
	testutil.FailErr(t, "remove branch file", os.Remove(filepath.Join(binding.Root, primaryDir, "a.txt")))
	if _, err := os.Stat(filepath.Join(binding.Root, primaryDir, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("removed branch path reappeared")
	}
}

func TestConcurrentClaimsReuseOneCompleteBranch(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "project")
	testutil.FailErr(t, "mkdir project", os.MkdirAll(primary, 0o755))
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(primary, "main.go"), []byte("package main\n"), 0o644))
	roots := []projectroot.RootRef{{ID: "p", Label: "app", Path: primary, IsPrimary: true}}
	mgr := workspace.NewManager(filepath.Join(base, "branches"), filepath.Join(base, "seeds"))

	type result struct {
		binding *workspace.Binding
		err     error
	}
	results := make(chan result, 2)
	var start sync.WaitGroup
	start.Add(1)
	for range 2 {
		go func() {
			start.Wait()
			binding, _, err := mgr.CreateWorkerWorkspaceFromSources(t.Context(), roots, roots, "p", "same-job")
			results <- result{binding: binding, err: err}
		}()
	}
	start.Done()
	first, second := <-results, <-results
	testutil.FailErr(t, "first concurrent claim", first.err)
	testutil.FailErr(t, "second concurrent claim", second.err)
	if first.binding.Root != second.binding.Root {
		t.Fatalf("branch roots differ: %q %q", first.binding.Root, second.binding.Root)
	}
	if body, err := os.ReadFile(filepath.Join(first.binding.Root, "main.go")); err != nil || string(body) != "package main\n" {
		t.Fatalf("shared branch body = %q err=%v", body, err)
	}
	meta, err := workspace.LoadJobMeta(enginepaths.MetaDirForBranchRoot(first.binding.Root))
	testutil.FailErr(t, "load shared branch metadata", err)
	if !meta.SnapshotComplete {
		t.Fatal("shared branch is incomplete")
	}
}

func TestCreateWorkerWorkspaceFromSourcesSingleRootFlat(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "project")
	testutil.FailErr(t, "mkdir project", os.MkdirAll(dir, 0o755))
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "only.txt"), []byte("x"), 0o644))
	roots := []projectroot.RootRef{{ID: "p", Label: "root", Path: dir, IsPrimary: true}}
	mgr := workspace.NewManager(filepath.Join(base, "branches"), filepath.Join(base, "seeds"))
	binding, _, err := mgr.CreateWorkerWorkspaceFromSources(context.Background(), roots, roots, "p", "job-1")
	testutil.FailErr(t, "CreateWorkerWorkspaceFromSources", err)
	if _, err := os.Stat(filepath.Join(binding.Root, "only.txt")); err != nil {
		t.Fatalf("flat snapshot missing file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(binding.Root, "root")); !os.IsNotExist(err) {
		t.Fatal("single-root sandbox must be flat")
	}
}
