package gitcmd

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/testutil"
)

// AdoptPlainWorkerView gives a worker's branch the managed view every write-leg worker has, over
// sources that hold no repository.
func AdoptPlainWorkerView(t *testing.T, worktree, workerID string) *privategit.Manager {
	t.Helper()
	mgr, err := privategit.NewManager(privategit.ManagerConfig{BackingDir: filepath.Join(t.TempDir(), "pgit")})
	testutil.FailErr(t, "new private Git manager", err)
	viewID := "view-" + workerID
	testutil.FailErr(t, "adopt worker view", mgr.AdoptMaterializedView(t.Context(), privategit.MaterializedViewBinding{
		ViewID: viewID, WorkerID: workerID, RootID: "primary", TrunkDir: t.TempDir(), WorktreeDir: worktree,
	}))
	t.Cleanup(func() { _ = mgr.ReleaseView(viewID) })
	return mgr
}
