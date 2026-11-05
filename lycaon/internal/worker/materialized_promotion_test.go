package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// A merge can only join the worker's edit to the source's if the checkpoint holds the bytes both
// started from, and a materialized claim hashed none of them.
func TestMaterializedWorkerPromotesThreeWayFromItsPristineBase(t *testing.T) {
	ctx := t.Context()
	source := t.TempDir()
	lines := []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}
	original := strings.Join(lines, "\n") + "\n"
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(source, "notes.txt"), []byte(original), 0o644))
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRootWithID(t, database, testdbseed.DefaultProjectID, "root-id", source)
	testdbseed.InsertSession(t, database, "parent", testdbseed.DefaultProjectID)
	dataDir := testbaseline.DataDir(t, database)
	selector := workspace.DefaultWorkspaceSelector()
	selector.BudgetFor = adapter.ZeroBudget
	manager := workspace.NewManager(filepath.Join(dataDir, "worker-branches")).WithSelector(selector)
	queue := NewSQLQueue(database, 4)
	queue.SetBaselineStore(sourceledger.New(database, filepath.Join(dataDir, "source-content")).Baselines)
	queue.SetWorkerWorkspaceManager(manager)
	id, err := queue.Enqueue(ctx, api.WorkerTask{
		ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: source, WorkspaceRootID: "root-id",
		Scope:     &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"."}},
		AgentType: "implementer", Prompt: "edit", Brief: "edit",
	})
	testutil.FailErr(t, "enqueue", err)
	t.Cleanup(func() { session.ClearTaskCheckpointPreview(id) })
	next, err := queue.ClaimNext(ctx, ClaimRequest{ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "claim next", err)
	task, err := queue.ClaimWorkerBranch(ctx, id)
	testutil.FailErr(t, "claim branch", err)
	task.ClaimToken = next.ClaimToken

	worker := append([]string(nil), lines...)
	worker[0] = "ONE"
	testutil.FailErr(t, "worker edit", os.WriteFile(filepath.Join(task.WorkspaceRoot, "notes.txt"), []byte(strings.Join(worker, "\n")+"\n"), 0o644))
	if won, err := queue.Complete(ctx, task, api.WorkerResult{Status: "complete"}); err != nil || !won {
		t.Fatalf("Complete: won=%v err=%v", won, err)
	}
	moved := append([]string(nil), lines...)
	moved[8] = "NINE"
	testutil.FailErr(t, "source moves", os.WriteFile(filepath.Join(source, "notes.txt"), []byte(strings.Join(moved, "\n")+"\n"), 0o644))

	service := &MergeService{Queue: queue, Store: queue, DataDir: dataDir}
	result, err := service.PromoteOverlay(ctx, "parent", id, api.PromoteOverlayInput{})
	testutil.FailErr(t, "promote", err)
	if result.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("promotion = %+v", result)
	}
	merged, err := os.ReadFile(filepath.Join(source, "notes.txt"))
	testutil.FailErr(t, "read merged", err)
	want := append([]string(nil), lines...)
	want[0], want[8] = "ONE", "NINE"
	if string(merged) != strings.Join(want, "\n")+"\n" {
		t.Fatalf("merged = %q", merged)
	}
}
