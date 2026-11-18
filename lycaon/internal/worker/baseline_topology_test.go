package worker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompletionRetainsBranchUntilBaselineTopologyCanBeLoaded(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "store.db")
	primary := t.TempDir()
	testdbseed.InsertSessionWithRoot(t, database, "parent", testdbseed.DefaultProjectID, primary)
	queue := NewSQLQueue(database, 1)
	queue.SetEventOutbox(eventoutbox.New(database, nil))
	id, err := queue.Enqueue(ctx, api.WorkerTask{ID: "topology", ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID, AgentType: "implementer", Prompt: "work", Brief: "work", WorkspacePath: primary, Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite}})
	testutil.FailErr(t, "enqueue", err)
	task, err := queue.ClaimNext(ctx, ClaimRequest{ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "poller"})
	testutil.FailErr(t, "claim", err)
	if task == nil {
		t.Fatal("worker not claimed")
	}
	branch := filepath.Join(testbaseline.DataDir(t, database), "worker-branches", id)
	testutil.FailErr(t, "create branch", os.MkdirAll(branch, 0o700))
	baseline := testbaseline.Durable(t, "root", database, id, branch)
	testutil.FailErr(t, "write change", os.WriteFile(filepath.Join(branch, "changed.txt"), []byte("retained"), 0o600))
	bound, err := queue.store.SetWorkerWorkspace(ctx, id, branch, baseline)
	testutil.FailErr(t, "bind branch", err)
	if !bound {
		t.Fatal("branch not bound")
	}
	task.WorkspaceRoot, task.WorkspaceBaselinePath = branch, baseline
	if committed, err := queue.Complete(ctx, task, api.WorkerResult{Summary: "done"}); err == nil || committed {
		t.Fatalf("missing topology completion = %v, %v", committed, err)
	}
	persisted, ok := queue.Get(id)
	if !ok || persisted.Status != api.WorkerStatusRunning || persisted.WorkspaceOverlayPath != "" {
		t.Fatalf("failed completion changed job: %+v", persisted)
	}
	body, err := os.ReadFile(filepath.Join(branch, "changed.txt"))
	testutil.FailErr(t, "read retained branch", err)
	if string(body) != "retained" {
		t.Fatalf("retained body = %q", body)
	}
	testbaseline.RecordBranch(t, "root", primary, branch)
	committed, err := queue.Complete(ctx, task, api.WorkerResult{Summary: "done"})
	testutil.FailErr(t, "retry completion", err)
	if !committed {
		t.Fatal("retry did not commit")
	}
	persisted, ok = queue.Get(id)
	if !ok || persisted.WorkspaceOverlayPath == "" {
		t.Fatalf("retry did not seal overlay: %+v", persisted)
	}
	reader, err := workspacebaseline.Open(ctx, persisted.WorkspaceOverlayPath, workspacebaseline.ContentStore(persisted.WorkspaceOverlayPath))
	testutil.FailErr(t, "open sealed overlay", err)
	defer func() { _ = reader.Close() }()
	if _, exists, err := reader.LookupIdentity(ctx, nil, "root", "changed.txt"); err != nil || !exists {
		t.Fatalf("sealed identity = %v, %v", exists, err)
	}
}
