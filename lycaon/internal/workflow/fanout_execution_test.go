package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPlannedFanoutDoesNotAdvancePastMissingOrPartialWork(t *testing.T) {
	mgr, _, _, dir := testManagerWithRegistry(t)
	ctx := t.Context()
	run, err := startRun(ctx, mgr, "sess-1", "security-survey", "2.0.0")
	testutil.FailErr(t, "start run", err)
	run.CurrentPhase = "execute"
	testutil.FailErr(t, "set execution phase", mgr.Store.Update(ctx, run))
	plan := FanoutPlan{Phase: "execute", MaxAttempts: 2, Legs: []FanoutPlanLeg{{ID: "leg-1", AgentType: "security-reviewer", Prompt: "first"}, {ID: "leg-2", AgentType: "security-reviewer", Prompt: "second"}}}
	testutil.FailErr(t, "stamp plan", mgr.Store.UpdateVars(ctx, run, dir, stampFanoutPlan(nil, plan)))
	var tasks []api.WorkerTask
	mgr.WorkerTasks = func(_ context.Context, id string) ([]api.WorkerTask, error) {
		if id != run.ID {
			t.Fatalf("ledger run=%s", id)
		}
		return tasks, nil
	}
	first := api.WorkerTask{ID: "one", AgentType: "security-reviewer", ParentSessionID: "sess-1", CreatedAt: time.Unix(1, 0)}
	testutil.FailErr(t, "bind first leg", mgr.BindWorkflowTask(ctx, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-1"},
	}, "leg-1", &first))
	if first.WorkflowRunID != run.ID || first.WorkflowPhase != "execute" {
		t.Fatalf("task binding=%#v", first)
	}
	first.Status = api.WorkerStatusPending
	tasks = append(tasks, first)
	if err := mgr.AssertWorkerTask(ctx, &first); err == nil {
		t.Fatal("accepted duplicate active leg")
	}
	tasks[0].Status = api.WorkerStatusComplete
	tasks[0].Result = &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "partial"}}
	testutil.FailErr(t, "partial proof", mgr.RecordWorkerTerminalProof(ctx, "sess-1", "one", "partial"))
	current, err := mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "read partial phase", err)
	if current.CurrentPhase != "execute" {
		t.Fatal("missing planned leg was ignored")
	}
	second := first
	second.ID, second.WorkflowWorkID, second.Status = "two", "leg-2", api.WorkerStatusComplete
	second.Result = &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}
	tasks = append(tasks, second)
	testutil.FailErr(t, "successful peer proof", mgr.RecordWorkerTerminalProof(ctx, "sess-1", "two", "complete"))
	current, err = mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "read peer phase", err)
	if current.CurrentPhase != "execute" {
		t.Fatal("last success hid a partial peer")
	}
	testutil.FailErr(t, "admit recovery", mgr.AssertWorkerTask(ctx, &first))
	retry := tasks[0]
	retry.ID, retry.CreatedAt = "retry", time.Unix(3, 0)
	tasks = append(tasks, retry)
	if err := mgr.AssertWorkerTask(ctx, &first); err == nil {
		t.Fatal("accepted attempt beyond recovery allowance")
	}
	testutil.FailErr(t, "exhausted proof", mgr.RecordWorkerTerminalProof(ctx, "sess-1", "retry", "partial"))
	current, err = mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "read settled phase", err)
	if current.CurrentPhase != "claims" {
		t.Fatalf("settled phase=%s", current.CurrentPhase)
	}
}
