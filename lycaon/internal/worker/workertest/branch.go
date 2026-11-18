package workertest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

// NewBranch reserves a branch inside the fixture's installation.
func NewBranch(t testing.TB, database db.Handle) string {
	t.Helper()
	root := enginepaths.WorkerBranchesRootUnder(db.DataDir(database))
	testutil.FailErr(t, "create worker branches", os.MkdirAll(root, 0700))
	branch, err := os.MkdirTemp(root, "fixture-") //nolint:usetesting // Branch lookup requires the installation root.
	testutil.FailErr(t, "create worker branch", err)
	return branch
}

// BindBranch captures and persists a branch through the production store.
func BindBranch(t testing.TB, database db.Handle, jobID, branch string) {
	t.Helper()
	roots, err := workspace.LoadBranchRoots(branch)
	testutil.FailErr(t, "load branch topology", err)
	root := db.DataDir(database)
	baselines := workspacebaseline.New(database, sourceblob.New(filepath.Join(root, "source-content")), filepath.Join(root, "worker-baselines"))
	baseline, err := baselines.Capture(t.Context(), jobID, workspacebaseline.Branch(roots, branch))
	testutil.FailErr(t, "capture worker baseline", err)
	bound, err := worker.NewSQLStore(database).SetWorkerWorkspace(t.Context(), jobID, branch, baseline)
	testutil.FailErr(t, "bind worker branch", err)
	if !bound {
		t.Fatal("worker branch was not bound")
	}
}
