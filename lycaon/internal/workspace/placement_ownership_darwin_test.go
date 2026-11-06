//go:build darwin

package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testvolume"
	"github.com/lycaon/lycaon/internal/workerplacement"
)

// Linux has no mount option that does this; its filesystems without owners are
// refused by the mode check in workerplacement.
func TestVolumeIgnoringOwnershipIsNotClonable(t *testing.T) {
	volume, ran := testvolume.New(t, testvolume.Clonable)
	if ran {
		return
	}
	project := volume.Dir(t, "project")
	writeTree(t, project, projectFiles)
	volume.RemountIgnoringOwnership(t)
	fixture := newPlacementFixture(t)
	binding, err := fixture.mgr.CreateWorkerWorkspace(t.Context(), project, "job")
	testutil.FailErr(t, "create", err)
	if binding.Preparation.Clonable || binding.Preparation.NotClonableReason != workerplacement.ReasonVolumeIgnoresOwnership {
		t.Fatalf("preparation = %+v", binding.Preparation)
	}
	if _, err := os.Lstat(filepath.Join(volume.Root, enginepaths.WorkerPlacementDirName(os.Getuid(), fixture.state))); !os.IsNotExist(err) {
		t.Fatalf("a folder was written to the volume: %v", err)
	}
}
