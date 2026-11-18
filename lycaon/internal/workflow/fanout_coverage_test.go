package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFanoutCoverageAccountsForEveryLegAndRecovery(t *testing.T) {
	plan := runstate.FanoutPlan{MaxAttempts: 2, Legs: []runstate.FanoutPlanLeg{{ID: "leg-1", AgentType: "reviewer"}, {ID: "leg-2", AgentType: "reviewer"}}}
	job := func(id, leg, status string, at int64) api.WorkerTask {
		return api.WorkerTask{ID: id, WorkflowPhase: "execute", WorkflowWorkID: leg, Status: api.WorkerStatusComplete, CreatedAt: time.Unix(at, 0), Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: status}}}
	}
	tasks := []api.WorkerTask{job("one", "leg-1", "partial", 1)}
	got := runstate.FanoutCoverage(plan, tasks, "execute")
	if got[0].Settled || got[1].Settled || got[1].Status != "not_started" {
		t.Fatalf("premature completion: %#v", got)
	}
	tasks = append(tasks, job("two", "leg-2", "complete", 2))
	got = runstate.FanoutCoverage(plan, tasks, "execute")
	if got[0].Settled || !got[1].Settled {
		t.Fatalf("last success hid partial peer: %#v", got)
	}
	tasks = append(tasks, job("retry", "leg-1", "partial", 3))
	got = runstate.FanoutCoverage(plan, tasks, "execute")
	if !got[0].Settled || got[0].Status != "partial" || len(got[0].Attempts) != 2 {
		t.Fatalf("recovery accounting: %#v", got)
	}
	if wrong := runstate.FanoutCoverage(plan, tasks, "other"); wrong[0].Status != "not_started" {
		t.Fatalf("cross-phase contamination: %#v", wrong)
	}
	tasks[2].Status = api.WorkerStatusFailed
	tasks[2].Result.CompletionReport.LegStatus = "complete"
	if failed := runstate.FanoutCoverage(plan, tasks, "execute"); failed[0].Status != "failed" || !failed[0].Settled {
		t.Fatalf("model completion masked failed execution: %#v", failed)
	}
}

func TestRegistryWorkflowReadyCheckerAndPlannedWorkFanoutBinding(t *testing.T) {
	mgr, sessStore, bpMgr, _ := testManager(t)
	planRun, err := startRun(t.Context(), mgr, "sess-1", "plan", "1.0.1")
	testutil.FailErr(t, "start plan run", err)
	bpPath := planRun.BlueprintPath
	seedValidPlanContent(t, bpMgr, bpPath)
	_, err = mgr.Controls.Cancel(t.Context(), planRun.ID, "done")
	testutil.FailErr(t, "cancel plan run", err)

	condReg, err := conditions.NewDefaultRegistry(registryDepsForTests(mgr, bpMgr, conditions.TestRegistryDeps()))
	testutil.FailErr(t, "new condition registry", err)
	mgr.SetConditionRegistry(condReg)

	zeroChecker := RegistryWorkflowReadyChecker{}
	ready, err := zeroChecker.ImplementWorkflowReady(t.Context(), "sess-1", bpPath)
	testutil.FailErr(t, "nil registry check", err)
	if ready {
		t.Fatal("nil registry reported ready")
	}

	checker := RegistryWorkflowReadyChecker{
		Registry: condReg,
		Sessions: sessStore.Sessions,
		Runs:     mgr.Store.Runs,
	}
	ready, err = checker.ImplementWorkflowReady(t.Context(), "sess-1", bpPath)
	testutil.FailErr(t, "build session check", err)
	if !ready {
		t.Fatal("build session without active spec run should be ready")
	}

	testutil.FailErr(t, "set spec posture", sessStore.Sessions.UpdateSession(t.Context(), "sess-1", func(s *api.Session) {
		s.Posture = api.SessionPostureSpec
	}))
	ready, err = checker.ImplementWorkflowReady(t.Context(), "sess-1", bpPath)
	testutil.FailErr(t, "spec session check", err)
	if ready {
		t.Fatal("unapproved spec blueprint reported ready")
	}

	testutil.FailErr(t, "restore build posture", sessStore.Sessions.UpdateSession(t.Context(), "sess-1", func(s *api.Session) {
		s.Posture = api.SessionPostureBuild
	}))

	tctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "sess-1"}}
	mgr.Fanout.PlannedWorkKnown = func(_ context.Context, sessionID, workID string) (bool, error) {
		if workID == "err-leg" {
			return false, errors.New("plan store offline")
		}
		return sessionID == "sess-1" && workID == "planned-leg", nil
	}
	task := api.WorkerTask{ParentSessionID: "sess-1", AgentType: "implementer"}
	testutil.FailErr(t, "bind known planned leg without active run", mgr.Fanout.BindWorkflowTask(t.Context(), tctx, "planned-leg", &task))
	if task.WorkflowWorkID != "planned-leg" {
		t.Fatalf("WorkflowWorkID = %q", task.WorkflowWorkID)
	}
	if err := mgr.Fanout.BindWorkflowTask(t.Context(), tctx, "unknown-leg", &task); toolrejection.AsToolReject(err) == nil {
		t.Fatalf("unknown leg without active run accepted: %v", err)
	}
	if err := mgr.Fanout.BindWorkflowTask(t.Context(), tctx, "err-leg", &task); err == nil {
		t.Fatal("lookup error swallowed")
	}

	run, err := startRun(t.Context(), mgr, "sess-1", "plan", "1.0.1")
	testutil.FailErr(t, "start active run", err)
	runTask := api.WorkerTask{
		ParentSessionID: "sess-1",
		WorkflowRunID:   run.ID,
		WorkflowPhase:   run.CurrentPhase,
		WorkflowWorkID:  "planned-leg",
		AgentType:       "implementer",
	}
	testutil.FailErr(t, "assert known planned leg in non-fanout phase", mgr.Fanout.AssertWorkerTask(t.Context(), &runTask))
	runTask.WorkflowWorkID = "unknown-leg"
	if err := mgr.Fanout.AssertWorkerTask(t.Context(), &runTask); toolrejection.AsToolReject(err) == nil {
		t.Fatalf("unknown leg in non-fanout phase accepted: %v", err)
	}
}
