//go:build darwin || linux

package branchretention

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testvolume"
	"github.com/lycaon/lycaon/internal/workspace"
)

func TestRetentionAndInventoryFindABranchOnAnotherVolume(t *testing.T) {
	volume := testvolume.New(t, testvolume.Movable)
	t.Cleanup(workspace.UseCopyAsCloneForTest())
	project := volume.Dir(t, "project")
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(project, "a.txt"), []byte("hello"), 0o600))
	state, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve state", err)
	branchRoot := enginepaths.WorkerBranchesRootUnder(state)
	mgr := workspace.NewManager(branchRoot, enginepaths.WorkerSeedsRootUnder(state))
	binding, err := mgr.CreateWorkerWorkspace(t.Context(), project, "job")
	testutil.FailErr(t, "create", err)

	inventory, err := Inventory(t.Context(), branchRoot)
	testutil.FailErr(t, "inventory", err)
	if !inventory.Present || inventory.Bytes <= 0 || inventory.Unavailable != 0 {
		t.Fatalf("inventory = %+v", inventory)
	}

	volume.Eject(t)
	deps := Deps{
		BranchRoot: branchRoot,
		Jobs: func(context.Context) (map[string]JobState, error) {
			return map[string]JobState{"job": {Sealed: true}}, nil
		},
		Evict: workspace.EvictJobTree,
		Now:   time.Now,
	}
	report, err := ReclaimSealed(t.Context(), deps)
	testutil.FailErr(t, "sweep while ejected", err)
	if report.EvictedIdle+report.EvictedBudget != 0 {
		t.Fatalf("swept an absent volume: %+v", report)
	}
	inventory, err = Inventory(t.Context(), branchRoot)
	testutil.FailErr(t, "inventory while ejected", err)
	if inventory.Present || inventory.Unavailable != 1 {
		t.Fatalf("inventory while ejected = %+v", inventory)
	}
	volume.Remount(t)

	report, err = ReclaimSealed(t.Context(), deps)
	testutil.FailErr(t, "sweep", err)
	if report.EvictedIdle+report.EvictedBudget != 1 {
		t.Fatalf("report = %+v", report)
	}
	if _, err := os.Lstat(binding.Root); !os.IsNotExist(err) {
		t.Fatalf("tree remains: %v", err)
	}
	if _, err := workspace.LoadJobMeta(enginepaths.MetaDirForBranchRoot(binding.Root)); err != nil {
		t.Fatalf("evicted branch lost its record: %v", err)
	}
	testutil.FailErr(t, "destroy", mgr.DestroyWorkerWorkspace(binding))
}
