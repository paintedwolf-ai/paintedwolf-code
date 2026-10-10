package upgradefixture

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/harnessfixture"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerFixtureReconstructsAfterOriginalWorkspaceRemoval(t *testing.T) {
	dataDir, root := t.TempDir(), t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(dataDir, "store.db"))
	projects := project.NewSQLRegistry(database)
	testutil.FailErr(t, "write baseline", os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0o600))
	attached, err := projects.Create(t.Context(), project.CreateParams{Roots: []project.AttachRootParams{{Path: root}}})
	testutil.FailErr(t, "attach fixture project", err)
	sessions := store.NewSQL(database)
	parent, err := sessions.Create(t.Context(), api.CreateSessionRequest{WorkspaceRootID: attached.Roots[0].ID}, attached.ID)
	testutil.FailErr(t, "create fixture session", err)
	queue := worker.NewSQLQueue(database, 2)
	queue.SetBaselineStore(sourceledger.New(database, filepath.Join(dataDir, "source-content")).Baselines)
	queue.SetProjectStore(projects)
	queue.SetWorkerWorkspaceManager(workspace.NewManager(filepath.Join(dataDir, "worker-branches"), t.TempDir()))
	prepared, err := harnessfixture.Prepare(t.Context(), queue, sessions, parent, attached.Roots[0], harnessfixture.Setup{
		Overlays: []harnessfixture.Overlay{{Label: "Retained history", Files: map[string]string{"README.md": "changed\n", "new.txt": "new\n"}}},
	}, nil)
	testutil.FailErr(t, "complete fixture worker", err)
	evidence := WorkerEvidence{JobID: prepared.Overlays[0].JobID, ParentSessionID: parent.ID, ChildSessionID: prepared.Overlays[0].ChildSessionID, Baseline: map[string]string{"README.md": digest([]byte("base\n"))}, Overlay: map[string]string{"README.md": digest([]byte("changed\n")), "new.txt": digest([]byte("new\n"))}}
	task, ok := queue.Get(evidence.JobID)
	if !ok {
		t.Fatal("completed worker missing")
	}
	testutil.FailErr(t, "remove original external project", os.RemoveAll(root))
	testutil.FailErr(t, "remove rebuildable branch tree", os.RemoveAll(task.WorkspaceRoot))
	readOnly, err := db.OpenReadOnly(t.Context(), filepath.Join(dataDir, "store.db"))
	testutil.FailErr(t, "open read-only verification database", err)
	defer func() { _ = readOnly.Close() }()
	testutil.FailErr(t, "reconstruct retained worker", VerifyWorkerHistory(t.Context(), readOnly, attached.ID, evidence))
	evidence.Overlay["new.txt"] = digest([]byte("wrong\n"))
	if err := VerifyWorkerHistory(t.Context(), readOnly, attached.ID, evidence); err == nil {
		t.Fatal("changed semantic expectation passed")
	}
}
