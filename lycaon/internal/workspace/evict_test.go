package workspace_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
)

func createBranch(t *testing.T, mgr *workspace.Manager, jobID string) (string, string) {
	t.Helper()
	project := t.TempDir()
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(project, "main.go"), []byte("package main\n"), 0o644))
	roots := []projectroot.RootRef{{ID: "primary", Path: project, IsPrimary: true}}
	binding, _, err := mgr.CreateWorkerWorkspaceFromSources(context.Background(), roots, roots, "primary", jobID)
	testutil.FailErr(t, "create branch", err)
	return project, binding.Root
}

func TestEvictJobTreeKeepsRecordAndRebuildRestores(t *testing.T) {
	branches := t.TempDir()
	mgr := workspace.NewManager(branches, t.TempDir())
	_, root := createBranch(t, mgr, "job-evict")
	if !workspace.BranchTreePresent(root) {
		t.Fatal("fresh branch must read as present")
	}

	testutil.FailErr(t, "evict", workspace.EvictJobTree(context.Background(), root))
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("tree survived eviction: %v", err)
	}
	if workspace.BranchTreePresent(root) {
		t.Fatal("evicted branch must not read as present")
	}
	roots, err := workspace.LoadBranchRoots(root)
	testutil.FailErr(t, "roots after eviction", err)
	if len(roots) != 1 || roots[0].ID != "primary" {
		t.Fatalf("roots = %+v", roots)
	}
	if _, err := workspace.LoadBranchLayout(root); err == nil {
		t.Fatal("layout must require the tree")
	}

	restored := false
	layout, err := mgr.RebuildWorkerWorkspace(context.Background(), root, func(_ context.Context, layout workspace.SandboxLayout, branchRoot string) error {
		restored = true
		if branchRoot != root || len(layout.Roots) != 1 {
			t.Fatalf("restore got %q %+v", branchRoot, layout)
		}
		return os.WriteFile(filepath.Join(branchRoot, "main.go"), []byte("rebuilt\n"), 0o644)
	})
	testutil.FailErr(t, "rebuild", err)
	if !restored || len(layout.Roots) != 1 {
		t.Fatalf("rebuild restored=%v layout=%+v", restored, layout)
	}
	if !workspace.BranchTreePresent(root) {
		t.Fatal("rebuilt branch must read as present")
	}
	if _, err := workspace.LoadBranchLayout(root); err != nil {
		testutil.FailErr(t, "layout after rebuild", err)
	}
	if workspace.BranchLastUsed(root).IsZero() {
		t.Fatal("rebuild must stamp last use")
	}
}

func TestEvictJobTreeRefusesWhileLeased(t *testing.T) {
	mgr := workspace.NewManager(t.TempDir(), t.TempDir())
	_, root := createBranch(t, mgr, "job-lease")
	release, err := workspace.AcquireBranchUse(context.Background(), root)
	testutil.FailErr(t, "lease", err)
	if err := workspace.EvictJobTree(context.Background(), root); !errors.Is(err, workspace.ErrBranchInUse) {
		t.Fatalf("evict under lease = %v want ErrBranchInUse", err)
	}
	release()
	testutil.FailErr(t, "evict after release", workspace.EvictJobTree(context.Background(), root))
}

func TestListJobTreesReportsPresenceSizeAndLastUse(t *testing.T) {
	branches := t.TempDir()
	mgr := workspace.NewManager(branches, t.TempDir())
	_, kept := createBranch(t, mgr, "job-kept")
	_, evicted := createBranch(t, mgr, "job-evicted")
	old := time.Now().Add(-48 * time.Hour)
	workspace.TouchBranchUse(kept)
	stamp := filepath.Join(enginepaths.MetaDirForBranchRoot(kept), "LAST_USED")
	testutil.FailErr(t, "age stamp", os.Chtimes(stamp, old, old))
	testutil.FailErr(t, "evict", workspace.EvictJobTree(context.Background(), evicted))

	trees, err := workspace.ListJobTrees(context.Background(), branches)
	testutil.FailErr(t, "list", err)
	byJob := map[string]workspace.JobTree{}
	for _, tree := range trees {
		byJob[tree.JobID] = tree
	}
	if len(byJob) != 2 {
		t.Fatalf("trees = %+v", trees)
	}
	if got := byJob["job-kept"]; !got.Present || got.AllocatedBytes <= 0 || got.LastUsed.After(old.Add(time.Second)) || len(got.Roots) != 1 {
		t.Fatalf("kept = %+v", got)
	}
	if got := byJob["job-evicted"]; got.Present || got.AllocatedBytes != 0 || len(got.Roots) != 1 {
		t.Fatalf("evicted = %+v", got)
	}
}
