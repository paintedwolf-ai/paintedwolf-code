package orchestration_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowblueprintfiles "github.com/lycaon/lycaon/internal/workflow/blueprintfiles"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/pkg/api"
)

type spyWorkflowRuns struct {
	inner     *workflow.RunManager
	afterMark func(ctx context.Context, runID, stage, output string)
}

func (s *spyWorkflowRuns) MarkTopologyStageComplete(ctx context.Context, runID, stage, output, designForkCriterion string) error {
	if err := s.inner.Phases.MarkTopologyStageComplete(ctx, runID, stage, output, designForkCriterion); err != nil {
		return err
	}
	if s.afterMark != nil {
		s.afterMark(ctx, runID, stage, output)
	}
	return nil
}

func (s *spyWorkflowRuns) AssertRunnable(ctx context.Context, runID string) error {
	return s.inner.Policy.AssertRunnable(ctx, runID)
}

func (s *spyWorkflowRuns) AssertWorkerTask(ctx context.Context, task *api.WorkerTask) error {
	return s.inner.Fanout.AssertWorkerTask(ctx, task)
}

func newWorkflowOrchestrator(t *testing.T, rec *recordingDelegation, spy *spyWorkflowRuns) (*orchestration.OrchestratorImpl, *workflow.RunManager, *session.Host, *delegation.MemoryStore, db.Handle, string) {
	t.Helper()
	mockCfg, err := llm.LoadMockConfig()
	testutil.FailErr(t, "llm.LoadMockConfig failed", err)

	sqlDB := testdbfixture.Open(t, "wf.db")

	sessStore := store.NewSQL(sqlDB)
	sessMgr := session.NewHost(sessStore, session.Models{Client: llm.NewMockProvider(mockCfg), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	delStore := delegation.NewMemoryStore()
	queue := worker.NewInMemoryQueue(10)

	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "workflow.RegistryFromDirs failed", err)
	runStore := workflowpersistence.New(sqlDB)
	runStore.Transactions.SetAuthzRecorder(authzcontext.SQLRecorder(sqlDB))
	wfMgr := workflow.NewManager(runStore, sessStore, manifests, nil)
	projectDir := t.TempDir()
	blueprintMgr := workflow.WireBlueprintDepsForTest(wfMgr, projectDir)
	blueprintMgr.Approvals = blueprint.NewApprovalStore(sqlDB, authzcontext.SQLRecorder(sqlDB))
	deps := conditions.TestRegistryDeps()
	deps.BlueprintContent = func(_ context.Context, projectDir, relPath string) (string, error) {
		return workflowblueprintfiles.ReadBlueprintFile(projectDir, relPath)
	}
	reg, err := conditions.NewDefaultRegistry(deps)
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	wfMgr.SetConditionRegistry(reg)

	if spy == nil {
		spy = &spyWorkflowRuns{inner: wfMgr}
	} else {
		spy.inner = wfMgr
	}
	queue.SetWorkflowDomains(&worker.WorkflowDomains{Runs: spy, Tasks: spy})

	gate := delegation.WorkflowDispatchGate{
		Inner: delegation.AllowGate{},
		Store: delStore,
		Runs:  spy,
	}
	delMgr := delegation.NewManager(delStore, queue, sessMgr, gate)
	if rec == nil {
		rec = &recordingDelegation{inner: delMgr, store: delStore, order: make([]string, 0, 8)}
	} else {
		rec.inner = delMgr
		rec.store = delStore
	}

	regAgents := loadAgentRegistryFromConfig(t)
	stock := extpackstest.StockCatalog(t)
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Delegation: rec,
		Store:      delStore,
		Agents:     regAgents,
		Workflows:  &orchestration.WorkflowRunLifecycle{Runs: wfMgr.Store.Runs, Starts: wfMgr.Starts, Controls: wfMgr.Controls, Policy: wfMgr.Policy, Topology: spy},
		Catalog:    func() (*extpacks.EffectiveCatalog, error) { return stock, nil },
	})
	return orch, wfMgr, sessMgr, delStore, sqlDB, projectDir
}

func createOrchestrateSession(t *testing.T, sqlDB db.Handle, sessMgr *session.Host, dir string) *api.Session {
	t.Helper()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	sess, err := sessMgr.Chats.CreateForProject(context.Background(), testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.CreateForProject failed", err)
	return sess
}

func runBugbashToExpand(
	t *testing.T,
	ctx context.Context,
	orch *orchestration.OrchestratorImpl,
	wfMgr *workflow.RunManager,
	sess *api.Session,
	dir string,
) *orchestration.RunResult {
	t.Helper()
	result, err := orch.Run(ctx, orchestration.RunRequest{
		SessionID:       sess.ID,
		WorkflowID:      "bugbash",
		WorkflowVersion: "1.1.0",
		Input:           map[string]any{"project_dir": dir, "project_id": testdbseed.DefaultProjectID},
	})
	testutil.FailErr(t, "orch.Run failed", err)
	run, err := wfMgr.Store.Runs.ActiveBySession(ctx, sess.ID)
	testutil.FailErr(t, "wfMgr.GetActive failed", err)
	if run == nil || run.CurrentPhase != "expand" {
		t.Fatalf("run after topology = %+v want expand", run)
	}
	return result
}

func TestOrchestratorRunCreatesWorkflowRun(t *testing.T) {
	ctx := context.Background()
	rec := &recordingDelegation{order: make([]string, 0, 8)}
	orch, wfMgr, sessMgr, store, sqlDB, dir := newWorkflowOrchestrator(t, rec, nil)
	sess := createOrchestrateSession(t, sqlDB, sessMgr, dir)

	result := runBugbashToExpand(t, ctx, orch, wfMgr, sess, dir)

	active, err := wfMgr.Store.Runs.ActiveBySession(ctx, sess.ID)
	testutil.FailErr(t, "wfMgr.GetActive failed", err)
	if active == nil {
		t.Fatal("expected active workflow run")
	}

	delegationID, ok := store.DelegationBySessionID(sess.ID)
	if !ok {
		t.Fatal("missing delegation for session")
	}
	del, err := store.Get(ctx, delegationID)
	testutil.FailErr(t, "store.Get failed", err)
	if del.WorkflowID != "bugbash" {
		t.Fatalf("workflow_id = %q", del.WorkflowID)
	}
	if del.WorkflowVersion != "1.1.0" {
		t.Fatalf("workflow_version = %q", del.WorkflowVersion)
	}
	if del.WorkflowRunID != active.ID {
		t.Fatalf("workflow_run_id = %q want %q", del.WorkflowRunID, active.ID)
	}
	if result == nil || len(result.StageOutputs) != 4 {
		t.Fatalf("stage outputs = %d", len(result.StageOutputs))
	}
}

func TestOrchestratorFailureSettlesWorkflowRun(t *testing.T) {
	ctx := t.Context()
	rec := &recordingDelegation{order: make([]string, 0, 8), failStage: "hunt_edges"}
	spy := &spyWorkflowRuns{}
	orch, wfMgr, sessMgr, _, sqlDB, dir := newWorkflowOrchestrator(t, rec, spy)
	sess := createOrchestrateSession(t, sqlDB, sessMgr, dir)

	_, err := orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID, WorkflowID: "bugbash", WorkflowVersion: "1.1.0",
		Input: map[string]any{"project_dir": dir, "project_id": testdbseed.DefaultProjectID},
	})
	if err == nil {
		t.Fatal("expected topology failure")
	}
	runs, err := wfMgr.Store.Runs.ListBySession(ctx, sess.ID, 10, nil)
	testutil.FailErr(t, "list workflow runs", err)
	if len(runs) != 1 || runs[0].Status != api.WorkflowRunStatusFailed {
		t.Fatalf("runs = %+v", runs)
	}
	if runs[0].Failure == nil || runs[0].Failure.Code != "TOPOLOGY_STAGE_FAILED" || runs[0].Failure.Stage != "hunt_edges" {
		t.Fatalf("workflow failure = %+v", runs[0].Failure)
	}
}

func TestOrchestratorMarkStageOnPipelineComplete(t *testing.T) {
	ctx := context.Background()
	rec := &recordingDelegation{order: make([]string, 0, 2)}
	spy := &spyWorkflowRuns{}
	orch, wfMgr, sessMgr, _, sqlDB, dir := newWorkflowOrchestrator(t, rec, spy)
	sess := createOrchestrateSession(t, sqlDB, sessMgr, dir)

	wfRun, err := wfMgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID:      "bugbash",
		WorkflowVersion: "1.1.0",
	})
	testutil.FailErr(t, "wfMgr.Starts.StartHuman failed", err)

	var marked bool
	spy.afterMark = func(ctx context.Context, runID, stage, output string) {
		if stage != "research" || runID != wfRun.ID {
			return
		}
		vars, err := wfMgr.Store.Runs.GetScaffoldVars(ctx, runID)
		testutil.FailErr(t, "wfMgr.Store.Runs.GetScaffoldVars failed", err)
		stages, _ := vars["topology_stages"].(map[string]any)
		entry, _ := stages["research"].(map[string]any)
		if entry == nil || entry["complete"] != true {
			t.Fatalf("topology_stages = %v", vars["topology_stages"])
		}
		marked = true
	}

	spec := orchestration.TopologySpec{
		Pattern: orchestration.TopologyPipeline,
		Task:    "partial",
		Pipeline: &orchestration.PipelineSpec{
			Stages: []orchestration.PipelineStage{
				{Name: "research", AgentProfile: orchestration.ProfileRepoResearcher},
			},
		},
	}
	_, err = orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  spec,
		Input: map[string]any{
			"project_dir":      dir,
			"project_id":       testdbseed.DefaultProjectID,
			"workflow_run_id":  wfRun.ID,
			"workflow_id":      "bugbash",
			"workflow_version": "1.1.0",
		},
	})
	testutil.FailErr(t, "orch.Run failed", err)
	if !marked {
		t.Fatal("expected research stage marked in workflow scaffold vars")
	}
}

func TestOrchestratorPauseRespectsAssertRunnable(t *testing.T) {
	ctx := context.Background()
	rec := &recordingDelegation{order: make([]string, 0, 8)}
	spy := &spyWorkflowRuns{}
	orch, wfMgr, sessMgr, _, sqlDB, dir := newWorkflowOrchestrator(t, rec, spy)
	sess := createOrchestrateSession(t, sqlDB, sessMgr, dir)

	wfRun, err := wfMgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID:      "bugbash",
		WorkflowVersion: "1.1.0",
	})
	testutil.FailErr(t, "wfMgr.Starts.StartHuman failed", err)

	spy.afterMark = func(ctx context.Context, runID, stage, output string) {
		if stage != "research" {
			return
		}
		if _, err := wfMgr.Controls.Pause(ctx, runID, "test_pause"); err != nil {
			testutil.FailErr(t, "wfMgr.Controls.Pause failed", err)
		}
	}

	spec := orchestration.TopologySpec{
		Pattern: orchestration.TopologyPipeline,
		Task:    "pause test",
		Pipeline: &orchestration.PipelineSpec{
			Stages: []orchestration.PipelineStage{
				{Name: "research", AgentProfile: orchestration.ProfileRepoResearcher},
				{Name: "plan", AgentProfile: orchestration.ProfileCoordinator, InputFrom: []string{"research"}},
			},
		},
	}
	_, err = orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  spec,
		Input: map[string]any{
			"project_dir":      dir,
			"project_id":       testdbseed.DefaultProjectID,
			"workflow_run_id":  wfRun.ID,
			"workflow_id":      "bugbash",
			"workflow_version": "1.1.0",
		},
	})
	if err == nil {
		t.Fatal("expected paused workflow to block dispatch")
	}
	if !strings.Contains(err.Error(), "paused") {
		t.Fatalf("err = %v", err)
	}
}

func TestOrchestratorStatusReflectsPhase(t *testing.T) {
	ctx := context.Background()
	rec := &recordingDelegation{order: make([]string, 0, 8)}
	orch, wfMgr, sessMgr, _, sqlDB, dir := newWorkflowOrchestrator(t, rec, nil)
	sess := createOrchestrateSession(t, sqlDB, sessMgr, dir)

	result := runBugbashToExpand(t, ctx, orch, wfMgr, sess, dir)

	status, err := orch.Status(ctx, result.RunID)
	testutil.FailErr(t, "orch.Status failed", err)
	if status.Phase == "pipeline" || status.Phase == "done" {
		t.Fatalf("status phase = %q want workflow current phase", status.Phase)
	}
	if status.Phase != "expand" {
		t.Fatalf("status phase = %q want expand", status.Phase)
	}
}

func TestOrchestratorCancelPropagatesWorkflowRun(t *testing.T) {
	ctx := context.Background()
	rec := &recordingDelegation{order: make([]string, 0, 8)}
	orch, wfMgr, sessMgr, _, sqlDB, dir := newWorkflowOrchestrator(t, rec, nil)
	sess := createOrchestrateSession(t, sqlDB, sessMgr, dir)

	result := runBugbashToExpand(t, ctx, orch, wfMgr, sess, dir)

	active, err := wfMgr.Store.Runs.ActiveBySession(ctx, sess.ID)
	testutil.FailErr(t, "wfMgr.GetActive failed", err)
	if active == nil {
		t.Fatal("expected active workflow run")
	}

	before, err := orch.Status(ctx, result.RunID)
	testutil.FailErr(t, "read awaiting workflow status", err)
	if before.Active || before.Phase != active.CurrentPhase {
		t.Fatalf("awaiting workflow status = %+v, want inactive topology at %q", before, active.CurrentPhase)
	}

	if err := orch.Cancel(ctx, result.RunID, orchestration.TerminationReasonHumanAbort); err != nil {
		testutil.FailErr(t, "orch.Cancel failed", err)
	}
	run, err := wfMgr.Store.Runs.Get(ctx, active.ID)
	testutil.FailErr(t, "wfMgr.Presentation.Get failed", err)
	if run.Status != api.WorkflowRunStatusCanceled {
		t.Fatalf("workflow status = %q", run.Status)
	}
	after, err := orch.Status(ctx, result.RunID)
	testutil.FailErr(t, "read canceled workflow status", err)
	if after.Active || after.Phase != string(api.WorkflowRunStatusCanceled) {
		t.Fatalf("canceled workflow still presents an executable phase: %+v", after)
	}
}
