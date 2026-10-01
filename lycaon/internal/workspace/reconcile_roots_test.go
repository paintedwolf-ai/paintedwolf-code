package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
)

func TestReconcileSandboxRootsRemovesUnregisteredProjects(t *testing.T) {
	branchRoot := t.TempDir()
	seedRoot := t.TempDir()
	live := t.TempDir()
	gone := t.TempDir()

	liveJob := seedSandbox(t, branchRoot, live, "job-live")
	goneJob := seedSandbox(t, branchRoot, gone, "job-gone")
	stray := filepath.Join(branchRoot, "not-a-project-key")
	testutil.FailErr(t, "mkdir stray", os.MkdirAll(stray, 0o755))

	liveSeed := filepath.Join(seedRoot, enginepaths.ProjectKey(live), "generations", "g1")
	goneSeed := filepath.Join(seedRoot, enginepaths.ProjectKey(gone), "generations", "g1")
	for _, dir := range []string{liveSeed, goneSeed} {
		testutil.FailErr(t, "mkdir seed", os.MkdirAll(dir, 0o755))
		testutil.FailErr(t, "write seed", os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644))
	}

	removed, err := workspace.ReconcileSandboxRoots(context.Background(), branchRoot, seedRoot, []string{live}, retainedJobIDs(nil))
	testutil.FailErr(t, "ReconcileSandboxRoots", err)
	if removed != 2 {
		t.Fatalf("removed = %d, want the orphan branch tree and the orphan seed", removed)
	}
	if _, err := os.Stat(goneJob); !os.IsNotExist(err) {
		t.Fatal("orphan project branch tree survived")
	}
	if _, err := os.Stat(filepath.Join(seedRoot, enginepaths.ProjectKey(gone))); !os.IsNotExist(err) {
		t.Fatal("orphan project seed survived")
	}
	for _, kept := range []string{liveJob, liveSeed, stray} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("live or unrelated path removed: %s: %v", kept, err)
		}
	}
}

func TestReconcileSandboxRootsToleratesMissingRoots(t *testing.T) {
	removed, err := workspace.ReconcileSandboxRoots(context.Background(),
		filepath.Join(t.TempDir(), "absent-branches"), filepath.Join(t.TempDir(), "absent-seeds"), nil, retainedJobIDs(nil))
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
	_, err := workspace.ReconcileSandboxRoots(t.Context(), branchRoot, "", []string{currentProject}, retainedJobIDs(jobs))
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
	_, err = workspace.ReconcileSandboxRoots(t.Context(), branchRoot, "", []string{currentProject}, retainedJobIDs(nil))
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
	_, err := workspace.ReconcileSandboxRoots(t.Context(), branches, "", nil, load)
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
			_, err = workspace.ReconcileSandboxRoots(t.Context(), branches, "", nil, retainedJobIDs(nil))
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
			_, err = workspace.ReconcileSandboxRoots(t.Context(), branches, "", nil, retainedJobIDs(nil))
			testutil.FailErr(t, "reclaim released orphan", err)
			if _, err := os.Stat(job); !os.IsNotExist(err) {
				t.Fatalf("released orphan remains: %v", err)
			}
		})
	}
}
