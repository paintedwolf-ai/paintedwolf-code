package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
)

func TestReconcileSandboxRootsRemovesUnregisteredProjects(t *testing.T) {
	branchRoot := t.TempDir()
	live := t.TempDir()
	gone := t.TempDir()

	liveJob := seedSandbox(t, branchRoot, live, "job-live")
	goneJob := seedSandbox(t, branchRoot, gone, "job-gone")
	stray := filepath.Join(branchRoot, "not-a-project-key")
	testutil.FailErr(t, "mkdir stray", os.MkdirAll(stray, 0o755))

	removed, err := workspace.ReconcileSandboxRoots(context.Background(), branchRoot, []string{live}, nil, retainedJobIDs(nil))
	testutil.FailErr(t, "ReconcileSandboxRoots", err)
	if removed != 1 {
		t.Fatalf("removed = %d, want the orphan branch tree", removed)
	}
	if _, err := os.Stat(goneJob); !os.IsNotExist(err) {
		t.Fatal("orphan project branch tree survived")
	}
	for _, kept := range []string{liveJob, stray} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("live or unrelated path removed: %s: %v", kept, err)
		}
	}
}

func TestReconcileSandboxRootsToleratesMissingRoots(t *testing.T) {
	removed, err := workspace.ReconcileSandboxRoots(context.Background(),
		filepath.Join(t.TempDir(), "absent-branches"), nil, nil, retainedJobIDs(nil))
	testutil.FailErr(t, "ReconcileSandboxRoots", err)
	if removed != 0 {
		t.Fatalf("removed = %d, want 0", removed)
	}
}

func TestReconcileSandboxRootsPreservesRetainedHistoryAfterProjectMoves(t *testing.T) {
	branchRoot, priorProject, currentProject := t.TempDir(), t.TempDir(), t.TempDir()
	unsealed := seedSandbox(t, branchRoot, priorProject, "unsealed")
	sealed, _ := seedSandboxPair(t, branchRoot, priorProject, "sealed")
	stale, _ := seedSandboxPair(t, branchRoot, priorProject, "stale")
	metaPath := filepath.Join(enginepaths.MetaDirForBranchRoot(sealed), "state.json")
	testutil.FailErr(t, "write retained topology", os.WriteFile(metaPath, []byte("retained topology"), 0o600))
	testutil.FailErr(t, "evict sealed tree", os.RemoveAll(sealed))
	jobs := map[string]struct{}{"sealed": {}, "unsealed": {}}
	_, err := workspace.ReconcileSandboxRoots(t.Context(), branchRoot, []string{currentProject}, nil, retainedJobIDs(jobs))
	testutil.FailErr(t, "reconcile relocated project", err)
	body, err := os.ReadFile(metaPath)
	testutil.FailErr(t, "read retained topology", err)
	if string(body) != "retained topology" {
		t.Fatal("retained topology changed")
	}
	if _, err := os.Stat(unsealed); err != nil {
		t.Fatalf("unsealed worker tree removed: %v", err)
	}
	for _, path := range []string{sealed, stale, enginepaths.MetaDirForBranchRoot(stale)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unretained worker path remains %s: %v", path, err)
		}
	}
	_, err = workspace.ReconcileSandboxRoots(t.Context(), branchRoot, []string{currentProject}, nil, retainedJobIDs(nil))
	testutil.FailErr(t, "reconcile deleted history", err)
	if _, err := os.Stat(filepath.Dir(sealed)); !os.IsNotExist(err) {
		t.Fatalf("unreferenced branch bucket remains: %v", err)
	}
}

func TestReconcileSandboxRootsLoadsOwnershipAfterEnumeratingCandidates(t *testing.T) {
	branches, prior := t.TempDir(), t.TempDir()
	job := seedSandbox(t, branches, prior, "provisioning")
	var publishedAfterEnumeration string
	load := func(context.Context) (map[string]struct{}, error) {
		publishedAfterEnumeration = seedSandbox(t, branches, prior, "published-later")
		return map[string]struct{}{"provisioning": {}}, nil
	}
	_, err := workspace.ReconcileSandboxRoots(t.Context(), branches, nil, nil, load)
	testutil.FailErr(t, "reconcile provisioning interleaving", err)
	for _, path := range []string{job, publishedAfterEnumeration} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("branch created before ownership lookup removed: %s: %v", path, err)
		}
	}
}

func TestReconcileSandboxRootsDefersHeldBranchLocks(t *testing.T) {
	for _, suffix := range []string{".use.lock", ".provision.lock"} {
		t.Run(suffix, func(t *testing.T) {
			branches, prior := t.TempDir(), t.TempDir()
			job, meta := seedSandboxPair(t, branches, prior, "held")
			lock, err := filelock.Open(job + suffix)
			testutil.FailErr(t, "open branch coordination lock", err)
			defer func() { _ = lock.Close() }()
			held, err := filelock.TryExclusive(lock)
			testutil.FailErr(t, "hold branch coordination lock", err)
			if !held {
				t.Fatal("branch coordination lock not acquired")
			}
			before, err := os.Stat(job + suffix)
			testutil.FailErr(t, "stat held lock", err)
			_, err = workspace.ReconcileSandboxRoots(t.Context(), branches, nil, nil, retainedJobIDs(nil))
			testutil.FailErr(t, "defer held branch", err)
			for _, path := range []string{job, meta} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("held branch removed: %s: %v", path, err)
				}
			}
			after, err := os.Stat(job + suffix)
			testutil.FailErr(t, "stat coordination inode after cleanup", err)
			if !os.SameFile(before, after) {
				t.Fatal("held coordination lock was replaced")
			}
			testutil.FailErr(t, "release branch coordination lock", filelock.Unlock(lock))
			_, err = workspace.ReconcileSandboxRoots(t.Context(), branches, nil, nil, retainedJobIDs(nil))
			testutil.FailErr(t, "reclaim released orphan", err)
			if _, err := os.Stat(job); !os.IsNotExist(err) {
				t.Fatalf("released orphan remains: %v", err)
			}
		})
	}
}

func TestResolveDispatchWorkspaceSelectionNarrowsRootsAndIgnoresTaskPathHints(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	testutil.FailErr(t, "mkdir subroot", os.MkdirAll(filepath.Join(rootA, "services", "api"), 0o755))
	testutil.FailErr(t, "mkdir sibling", os.MkdirAll(filepath.Join(rootA, "services", "billing"), 0o755))

	attached := []projectroot.RootRef{
		{ID: "primary", Label: "primary", Path: rootA, IsPrimary: true},
		{ID: "docs", Label: "docs", Path: rootB},
	}

	// TaskPathHints alone do not narrow or expand selection when WorkspaceSelection is empty.
	grantsAll, refsAll, err := workspace.ResolveDispatchWorkspaceSelection(
		attached, adapter.WorkspaceSelection{}, []string{"primary/services/api/handler.go"},
	)
	testutil.FailErr(t, "ResolveDispatchWorkspaceSelection default", err)
	if len(grantsAll) != 2 || len(refsAll) != 2 || refsAll[0].Path != filepath.Clean(rootA) {
		t.Fatalf("expected default selection to include all attached roots unchanged, got %+v", refsAll)
	}

	// Explicit WorkspaceSelection narrows root and subroot while ignoring TaskPathHints to other roots.
	grantsNarrow, refsNarrow, err := workspace.ResolveDispatchWorkspaceSelection(
		attached,
		adapter.WorkspaceSelection{
			RootIDs:  []string{"primary"},
			Subroots: map[string]string{"primary": "services/api"},
		},
		[]string{"docs/README.md", "primary/services/billing/invoice.go"},
	)
	testutil.FailErr(t, "ResolveDispatchWorkspaceSelection narrow", err)
	wantSubPath := filepath.Join(filepath.Clean(rootA), "services", "api")
	if len(grantsNarrow) != 1 || len(refsNarrow) != 1 || grantsNarrow[0].Subroot != "services/api" || refsNarrow[0].Path != wantSubPath {
		t.Fatalf("unexpected narrowed roots: grants=%+v refs=%+v", grantsNarrow, refsNarrow)
	}

	// Unauthorized root selection must be rejected.
	if _, _, err := workspace.ResolveDispatchWorkspaceSelection(
		attached, adapter.WorkspaceSelection{RootIDs: []string{"ungranted"}}, nil,
	); err == nil {
		t.Fatal("expected unauthorized root selection to fail")
	}

	// Verify real Manager.CreateWorkerWorkspaceSelected and ReconcileSelectedSandboxRoots wire WorkspaceSelection.
	testutil.FailErr(t, "write api handler", os.WriteFile(filepath.Join(rootA, "services", "api", "handler.go"), []byte("package api\n"), 0o644))
	testutil.FailErr(t, "write billing invoice", os.WriteFile(filepath.Join(rootA, "services", "billing", "invoice.go"), []byte("package billing\n"), 0o644))
	testutil.FailErr(t, "write docs readme", os.WriteFile(filepath.Join(rootB, "README.md"), []byte("# docs\n"), 0o644))

	branches := t.TempDir()
	mgr := workspace.NewManager(branches)
	narrowSel := adapter.WorkspaceSelection{
		RootIDs:  []string{"primary"},
		Subroots: map[string]string{"primary": "services/api"},
	}
	binding, layout, err := mgr.CreateWorkerWorkspaceSelected(
		t.Context(), attached, narrowSel,
		[]string{"docs/README.md", "primary/services/billing/invoice.go"},
		"primary", "selected-job",
	)
	testutil.FailErr(t, "CreateWorkerWorkspaceSelected", err)
	defer func() { _ = mgr.DestroyWorkerWorkspace(binding) }()

	if len(layout.Roots) != 1 || layout.Roots[0].ID != "primary" {
		t.Fatalf("expected narrowed layout to contain only primary, got %+v", layout.Roots)
	}
	if _, err := os.Stat(filepath.Join(binding.Root, "handler.go")); err != nil {
		t.Fatalf("expected narrowed subroot file handler.go in worker workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(binding.Root, "invoice.go")); !os.IsNotExist(err) {
		t.Fatalf("unselected sibling subroot file must not exist in narrowed workspace: %v", err)
	}
	if _, err := workspace.ReconcileSelectedSandboxRoots(t.Context(), branches, attached, narrowSel, nil, nil, retainedJobIDs(map[string]struct{}{"selected-job": {}})); err != nil {
		testutil.FailErr(t, "ReconcileSelectedSandboxRoots", err)
	}
}
