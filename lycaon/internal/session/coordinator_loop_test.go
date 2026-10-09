//go:build integration

package session_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/reenter"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func wireImplementConvergenceHooks(t *testing.T, mgr *session.Manager, wfMgr *workflow.RunManager, q session.WorkerCycleLister) {
	t.Helper()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		WorkerCycleIdle: func(projectID, sessionID, completingJobID string) (bool, error) {
			return session.ParentSessionWorkerCycleIdle(context.Background(), q, projectID, sessionID, completingJobID)
		},
	})
	testutil.FailErr(t, "NewDefaultRegistry", err)
	wfMgr.SetConditionRegistry(reg)
	wfMgr.PhaseReenterHook = func(ctx context.Context, rc *workflow.RunContext, def workflowdef.PhaseDef) {
		if rc == nil {
			return
		}
		kickID := strings.TrimSpace(def.OnReenter.InjectKick)
		if kickID == "" {
			return
		}
		if id, ok := anchor.ParseID(kickID); ok {
			mgr.Emit(ctx, rc.SessionID, id, mgr.CoordinatorEnvelopeForWorkerCycleTerminal(ctx, rc.SessionID, ""))
		}
	}
	wfMgr.OnPhaseAutoAdvanced = func(ctx context.Context, sessionID, runID, previousPhase, newPhase string) {
		manifest, err := wfMgr.ManifestForRunID(ctx, runID)
		if err == nil {
			if _, ok := workflow.ReenterLegForAdvance(manifest, previousPhase, newPhase, sessionID); ok {
				reenter.NudgeOnManifestReenter(ctx, mgr, sessionID, manifest, previousPhase, newPhase)
				return
			}
		}
		if strings.TrimSpace(previousPhase) == "" && strings.TrimSpace(newPhase) != "" {
			mgr.NudgeCoordinatorLoop(ctx, sessionID, anchor.PhaseAdvanced, anchor.PhaseAdvanced, "", anchor.Envelope{})
			return
		}
		if strings.TrimSpace(previousPhase) != strings.TrimSpace(newPhase) {
			return
		}
		mgr.NudgeCoordinatorLoop(ctx, sessionID, anchor.PhaseAdvanced, anchor.PhaseAdvanced, "", anchor.Envelope{})
	}
	wfMgr.PhaseEnterHook = func(ctx context.Context, rc *workflow.RunContext, def workflowdef.PhaseDef) {
		if rc == nil {
			return
		}
		mgr.EmitMatch(ctx, rc.SessionID, anchor.PhaseEntered, anchor.Envelope{}, anchor.MatchContext{
			Surface:  "phase",
			Phase:    rc.Phase,
			Workflow: rc.WorkflowID,
		})
	}
}

// drainCoordinatorAsyncTurnsOnCleanup prevents writes during temp-dir cleanup.
func drainCoordinatorAsyncTurnsOnCleanup(t *testing.T, mgr *session.Manager) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		mgr.WaitForCoordinatorAsyncTurns(ctx)
	})
}

func setImplementRunPhase(t *testing.T, wfStore *workflow.SQLStore, run *wire.WorkflowRun, projectDir, phase string) {
	t.Helper()
	ctx := context.Background()
	vars, err := wfStore.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	run.CurrentPhase = phase
	testutil.FailErr(t, "CommitState", wfStore.CommitState(ctx, run, projectDir, vars))
}

type loopFixture struct {
	mgr   *session.Manager
	wfMgr *workflow.RunManager
	store *store.SQL
	sess  *wire.Session
}

func setupLoopFixture(t *testing.T, cfg settings.SessionLimits) loopFixture {
	t.Helper()
	testutil.SkipIfShort(t, "coordinator async loop integration")
	sqlDB := testdbfixture.Open(t, "loop wake.db")

	store := store.NewSQL(sqlDB)
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ack"}}}))
	mgr := session.NewManager(store, rec, tools.NewStubRegistry(), cfg)
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)
	mgr.SetAgentRegistry(agents)
	testutil.FailErr(t, "install anchor registry", mgr.InstallAnchorRegistry())
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	wirePromptTestManager(t, mgr)

	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "workflow.RegistryFromDirs failed", err)
	wfStore := workflow.NewSQLStore(sqlDB)
	wfMgr := workflow.NewManager(wfStore, store, manifestRegistry, nil)
	wfMgr.SessionScaffold = workflow.NewSessionScaffoldSQLStore(sqlDB)
	dir := t.TempDir()
	blueprintStore := blueprint.NewFileStoreForTest(dir)
	blueprintMgr := blueprint.NewManager(blueprintStore)
	wfMgr.BlueprintCreate = blueprint.WorkflowBlueprintCreator{Manager: blueprintMgr}
	wfMgr.BlueprintGet = blueprintMgr
	mgr.SetWorkflowSessionView(wfMgr, workflow.PolicySource(wfMgr))
	mgr.SetLoopWorkflowSource(wfMgr)

	ctx := context.Background()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	if _, err := wfMgr.StartHuman(ctx, sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0", Request: "Exercise the coordinator loop",
	}); err != nil {
		testutil.FailErr(t, "wfMgr.StartHuman failed", err)
	}
	run, err := wfMgr.GetActive(ctx, sess.ID)
	if err != nil || run == nil {
		t.Fatal("missing active run")
	}
	run.CurrentPhase = "research"
	run.Status = wire.WorkflowRunStatusRunning
	if err := wfMgr.Store.Update(ctx, run); err != nil {
		testutil.FailErr(t, "wfMgr.Store.Update failed", err)
	}
	drainCoordinatorAsyncTurnsOnCleanup(t, mgr)
	return loopFixture{mgr: mgr, wfMgr: wfMgr, store: store, sess: sess}
}

func TestShouldLoopWakeDeniesHumanApprovalAwaiting(t *testing.T) {
	fix := setupLoopFixture(t, settings.DefaultSessionLimits())
	ctx := context.Background()
	run, err := fix.wfMgr.GetActive(ctx, fix.sess.ID)
	if err != nil || run == nil {
		t.Fatal("missing run")
	}
	run.CurrentPhase = "approve"
	if err := fix.wfMgr.Store.Update(ctx, run); err != nil {
		testutil.FailErr(t, "fix.wfMgr.Store.Update failed", err)
	}
	vars, err := fix.wfMgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars = workflow.StampHumanApprovalPhase(vars, &workflowdef.HumanApprovalConfig{}, run.BlueprintPath)
	vars = workflow.SetHumanApprovalReady(vars, true)
	testutil.FailErr(t, "UpdateVars", fix.wfMgr.Store.UpdateVars(ctx, run, t.TempDir(), vars))
	allow, reason, err := fix.mgr.ShouldLoopWake(ctx, fix.sess.ID, anchor.LegFinished)
	testutil.FailErr(t, "fix.mgr.ShouldLoopWake failed", err)
	if allow {
		t.Fatal("expected deny while human approval is awaiting")
	}
	if reason != "human_approval_awaiting" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestShouldLoopWakeIgnoresApprovePhaseName(t *testing.T) {
	fix := setupLoopFixture(t, settings.DefaultSessionLimits())
	ctx := context.Background()
	run, err := fix.wfMgr.GetActive(ctx, fix.sess.ID)
	if err != nil || run == nil {
		t.Fatal("missing run")
	}
	run.CurrentPhase = "approve"
	if err := fix.wfMgr.Store.Update(ctx, run); err != nil {
		testutil.FailErr(t, "fix.wfMgr.Store.Update failed", err)
	}
	allow, reason, err := fix.mgr.ShouldLoopWake(ctx, fix.sess.ID, anchor.LegFinished)
	testutil.FailErr(t, "fix.mgr.ShouldLoopWake failed", err)
	if reason == "approve_phase" || reason == "human_approval_awaiting" {
		t.Fatalf("phase name must not deny, reason=%q", reason)
	}
	if !allow {
		t.Fatalf("allow=%v reason=%q want allow without awaiting vars", allow, reason)
	}
}

func TestShouldLoopWakeDeniesWhenBusy(t *testing.T) {
	fix := setupLoopFixture(t, settings.DefaultSessionLimits())
	ctx := context.Background()
	if err := fix.store.SetSessionStatus(ctx, fix.sess.ID, wire.SessionStatusBusy); err != nil {
		testutil.FailErr(t, "fix.store.SetSessionStatus failed", err)
	}
	finishExecution := fix.mgr.BeginPromptExecutionForTest(t.Context(), fix.sess.ID)
	defer finishExecution()
	allow, reason, err := fix.mgr.ShouldLoopWake(ctx, fix.sess.ID, anchor.LegFinished)
	testutil.FailErr(t, "fix.mgr.ShouldLoopWake failed", err)
	if allow {
		t.Fatal("expected deny while busy")
	}
	if reason != "session_busy" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestShouldLoopWakeRespectsBudget(t *testing.T) {
	cfg := settings.DefaultSessionLimits()
	cfg.MaxCoordinatorLoopCycles = 1
	fix := setupLoopFixture(t, cfg)
	ctx := context.Background()
	run, _ := fix.wfMgr.GetActive(ctx, fix.sess.ID)
	if run == nil {
		t.Fatal("missing run")
	}
	fix.mgr.ResetLoopBudget(run.ID)
	if !fix.mgr.TryConsumeLoopBudgetForTest(ctx, fix.sess.ID, run.ID) {
		t.Fatal("expected first consume")
	}
	if fix.mgr.TryConsumeLoopBudgetForTest(ctx, fix.sess.ID, run.ID) {
		t.Fatal("budget should be exhausted")
	}
}

func TestShouldLoopWakeDeniesPendingDecision(t *testing.T) {
	fix := setupLoopFixture(t, settings.DefaultSessionLimits())
	ctx := context.Background()
	run, _ := fix.wfMgr.GetActive(ctx, fix.sess.ID)
	if run == nil {
		t.Fatal("missing run")
	}
	vars := map[string]any{
		"user_decision": map[string]any{
			"implement": map[string]any{"pending": true, "prompt": "choose", "options": []any{"yes", "no"}},
		},
	}
	if err := fix.wfMgr.Store.UpdateVars(ctx, run, fix.sess.WorkspacePath, vars); err != nil {
		testutil.FailErr(t, "fix.wfMgr.Store.UpdateVars failed", err)
	}
	allow, reason, err := fix.mgr.ShouldLoopWake(ctx, fix.sess.ID, anchor.LegFinished)
	testutil.FailErr(t, "fix.mgr.ShouldLoopWake failed", err)
	if allow {
		t.Fatal("expected deny with pending decision")
	}
	if reason != "pending_user_input" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestLoopDefersUntilIdle(t *testing.T) {
	fix := setupLoopFixture(t, settings.DefaultSessionLimits())
	ctx := context.Background()
	if err := fix.store.SetSessionStatus(ctx, fix.sess.ID, wire.SessionStatusBusy); err != nil {
		testutil.FailErr(t, "fix.store.SetSessionStatus failed", err)
	}
	finishExecution := fix.mgr.BeginPromptExecutionForTest(t.Context(), fix.sess.ID)
	defer finishExecution()
	fix.mgr.NudgeCoordinatorLoop(ctx, fix.sess.ID, anchor.LegFinished, anchor.LegFinished, "leg-1", anchor.Envelope{})
	if _, ok := fix.mgr.PendingLoopNudgeForTest(fix.sess.ID); !ok {
		t.Fatal("expected deferred loop wake")
	}
	if err := fix.store.SetSessionStatus(ctx, fix.sess.ID, wire.SessionStatusIdle); err != nil {
		testutil.FailErr(t, "fix.store.SetSessionStatus failed", err)
	}
	finishExecution()
	fix.mgr.DrainLoopPendingForTest(ctx, fix.sess.ID)
	testutil.WaitFor(t, 3*time.Second, func() bool {
		msgs, err := fix.store.GetMessages(ctx, fix.sess.ID)
		if err != nil {
			return false
		}
		for _, msg := range msgs {
			if msg.Kind == wire.MessageKindHostLoopWake {
				return true
			}
		}
		return false
	})
}

func TestImplementWorkerSummaryLoopWakesWithCompletingJobStillRunning(t *testing.T) {
	root := configlayout.FindModuleRoot()
	sqlDB := testdbfixture.Open(t, "implement-autocontinue-running.db")

	store := store.NewSQL(sqlDB)
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "traced"}}}))
	mgr := session.NewManager(store, rec, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)
	mgr.SetAgentRegistry(agents)
	testutil.FailErr(t, "install anchor registry", mgr.InstallAnchorRegistry())
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))

	wfStore := workflow.NewSQLStore(sqlDB)
	bundledDir := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "workflows")
	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	wfMgr := workflow.NewManager(wfStore, store, manifestRegistry, nil)
	wfMgr.Resolver = workflow.ManifestResolver{}
	q := worker.NewInMemoryQueue(4)
	mgr.SetWorkerQueue(q)
	wireImplementConvergenceHooks(t, mgr, wfMgr, q)
	mgr.SetWorkflowSessionView(wfMgr, workflow.PolicySource(wfMgr))
	mgr.SetLoopWorkflowSource(wfMgr)

	ctx := context.Background()
	dir := t.TempDir()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	drainCoordinatorAsyncTurnsOnCleanup(t, mgr)
	ref, err := workflowdef.LoadRegistryConfig(extpacks.OnDisk(bundledDir))
	testutil.FailErr(t, "LoadRegistryConfig", err)
	run, err := wfMgr.StartAmbient(ctx, sess.ID, ref.ID, ref.Version)
	testutil.FailErr(t, "StartAmbient", err)
	setImplementRunPhase(t, wfStore, run, sess.WorkspacePath, "work")
	child, err := store.CreateChild(ctx, sess, wire.SpawnChildRequest{AgentType: orchestration.ProfilePathExplorer})
	testutil.FailErr(t, "create child", err)

	jobID, err := q.Enqueue(ctx, wire.WorkerTask{
		ParentSessionID: sess.ID,
		ChildSessionID:  child.ID,
		ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: orchestration.ProfilePathExplorer,
		Prompt:    "find files",
		Brief:     "fixture",
	})
	testutil.FailErr(t, "Enqueue", err)
	claimed, err := q.ClaimNext(ctx, worker.ClaimRequest{ExecutionTarget: wire.ExecutionTargetLocal})
	testutil.FailErr(t, "ClaimNext", err)
	if claimed.ID != jobID {
		t.Fatalf("claimed = %s want %s", claimed.ID, jobID)
	}

	if _, err := mgr.AppendWorkerSummary(ctx, sess.ID, session.WorkerSummaryInput{
		Summary:        "Found linux_cli_adventure.html",
		JobID:          jobID,
		AgentType:      orchestration.ProfilePathExplorer,
		ChildSessionID: child.ID,
		Status:         "complete",
		CompletedAt:    autoContinuePtrTime(time.Now().UTC()),
	}); err != nil {
		testutil.FailErr(t, "append worker summary", err)
	}
	simulateWorkerJobComplete(t, mgr, q, jobID)

	testutil.WaitFor(t, 5*time.Second, func() bool {
		msgs, err := store.GetMessages(ctx, sess.ID)
		if err != nil {
			return false
		}
		for _, msg := range msgs {
			if msg.Kind == wire.MessageKindHostLoopWake {
				return true
			}
		}
		return false
	})
}

func TestImplementWorkerCompleteFiresSingleLoopWake(t *testing.T) {
	root := configlayout.FindModuleRoot()
	sqlDB := testdbfixture.Open(t, "implement-single-autocontinue.db")

	store := store.NewSQL(sqlDB)
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "synthesized"}}}))
	mgr := session.NewManager(store, rec, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)
	mgr.SetAgentRegistry(agents)
	testutil.FailErr(t, "install anchor registry", mgr.InstallAnchorRegistry())
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))

	wfStore := workflow.NewSQLStore(sqlDB)
	bundledDir := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "workflows")
	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	wfMgr := workflow.NewManager(wfStore, store, manifestRegistry, nil)
	wfMgr.Resolver = workflow.ManifestResolver{}
	q := worker.NewInMemoryQueue(4)
	mgr.SetWorkerQueue(q)
	wireImplementConvergenceHooks(t, mgr, wfMgr, q)
	mgr.SetWorkflowSessionView(wfMgr, workflow.PolicySource(wfMgr))
	mgr.SetLoopWorkflowSource(wfMgr)

	ctx := context.Background()
	dir := t.TempDir()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	drainCoordinatorAsyncTurnsOnCleanup(t, mgr)
	ref, err := workflowdef.LoadRegistryConfig(extpacks.OnDisk(bundledDir))
	testutil.FailErr(t, "LoadRegistryConfig", err)
	run, err := wfMgr.StartAmbient(ctx, sess.ID, ref.ID, ref.Version)
	testutil.FailErr(t, "StartAmbient", err)
	setImplementRunPhase(t, wfStore, run, sess.WorkspacePath, "work")
	child, err := store.CreateChild(ctx, sess, wire.SpawnChildRequest{AgentType: orchestration.ProfilePathExplorer})
	testutil.FailErr(t, "create child", err)

	jobID, err := q.Enqueue(ctx, wire.WorkerTask{
		ParentSessionID: sess.ID,
		ChildSessionID:  child.ID,
		ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: orchestration.ProfilePathExplorer,
		Prompt:    "find files",
		Brief:     "fixture",
	})
	testutil.FailErr(t, "Enqueue", err)
	claimed, err := q.ClaimNext(ctx, worker.ClaimRequest{ExecutionTarget: wire.ExecutionTargetLocal})
	testutil.FailErr(t, "ClaimNext", err)
	if claimed.ID != jobID {
		t.Fatalf("claimed = %s want %s", claimed.ID, jobID)
	}

	if _, err := mgr.AppendWorkerSummary(ctx, sess.ID, session.WorkerSummaryInput{
		Summary:        "Found linux_cli_adventure.html",
		JobID:          jobID,
		AgentType:      orchestration.ProfilePathExplorer,
		ChildSessionID: child.ID,
		Status:         "complete",
		CompletedAt:    autoContinuePtrTime(time.Now().UTC()),
	}); err != nil {
		testutil.FailErr(t, "append research summary", err)
	}
	simulateWorkerJobComplete(t, mgr, q, jobID)

	testutil.WaitFor(t, 5*time.Second, func() bool {
		msgs, err := store.GetMessages(ctx, sess.ID)
		if err != nil {
			return false
		}
		autoCount := 0
		for _, msg := range msgs {
			if msg.Kind == wire.MessageKindHostLoopWake {
				autoCount++
			}
		}
		return autoCount >= 1
	})

	drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr.WaitForCoordinatorAsyncTurns(drainCtx)

	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	autoCount := 0
	for _, msg := range msgs {
		if msg.Kind == wire.MessageKindHostLoopWake {
			autoCount++
		}
	}
	if autoCount != 1 {
		t.Fatalf("host loop wake turns = %d want 1", autoCount)
	}
	settled, err := store.Get(ctx, sess.ID)
	testutil.FailErr(t, "read session after worker synthesis", err)
	if settled.Status != wire.SessionStatusIdle {
		t.Fatalf("status after worker synthesis = %q want idle", settled.Status)
	}
}

func TestImplementWorkerSummaryLoopWakesCoordinator(t *testing.T) {
	root := configlayout.FindModuleRoot()
	sqlDB := testdbfixture.Open(t, "implement-autocontinue.db")

	store := store.NewSQL(sqlDB)
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "traced"}}}))
	mgr := session.NewManager(store, rec, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)
	mgr.SetAgentRegistry(agents)
	testutil.FailErr(t, "install anchor registry", mgr.InstallAnchorRegistry())
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))

	wfStore := workflow.NewSQLStore(sqlDB)
	bundledDir := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "workflows")
	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	wfMgr := workflow.NewManager(wfStore, store, manifestRegistry, nil)
	wfMgr.Resolver = workflow.ManifestResolver{}
	q := worker.NewInMemoryQueue(4)
	mgr.SetWorkerQueue(q)
	wireImplementConvergenceHooks(t, mgr, wfMgr, q)
	mgr.SetWorkflowSessionView(wfMgr, workflow.PolicySource(wfMgr))
	mgr.SetLoopWorkflowSource(wfMgr)

	ctx := context.Background()
	dir := t.TempDir()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	drainCoordinatorAsyncTurnsOnCleanup(t, mgr)
	ref, err := workflowdef.LoadRegistryConfig(extpacks.OnDisk(bundledDir))
	testutil.FailErr(t, "LoadRegistryConfig", err)
	run, err := wfMgr.StartAmbient(ctx, sess.ID, ref.ID, ref.Version)
	testutil.FailErr(t, "StartAmbient", err)
	setImplementRunPhase(t, wfStore, run, sess.WorkspacePath, "work")
	child, err := store.CreateChild(ctx, sess, wire.SpawnChildRequest{AgentType: orchestration.ProfileImplementer, Prompt: "build game.py"})
	testutil.FailErr(t, "store.CreateChild failed", err)
	if err := store.AppendMessages(ctx, child.ID,
		wire.Message{
			Role: wire.MessageRoleAssistant,
			ToolCalls: []wire.ToolCall{
				{ID: "tc1", Name: "write", Args: map[string]any{"path": "game.py"}},
			},
		},
		wire.Message{Role: wire.MessageRoleTool, Content: "Wrote game.py"},
	); err != nil {
		testutil.FailErr(t, "store.AppendMessages failed", err)
	}

	jobID, err := q.Enqueue(ctx, wire.WorkerTask{
		ParentSessionID: sess.ID,
		ChildSessionID:  child.ID,
		ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: orchestration.ProfileImplementer,
		Prompt:    "build game.py",
		Brief:     "fixture",
	})
	testutil.FailErr(t, "Enqueue", err)
	claimed, err := q.ClaimNext(ctx, worker.ClaimRequest{ExecutionTarget: wire.ExecutionTargetLocal})
	testutil.FailErr(t, "ClaimNext", err)
	if claimed.ID != jobID {
		t.Fatalf("claimed = %s want %s", claimed.ID, jobID)
	}

	if _, err := mgr.AppendWorkerSummary(ctx, sess.ID, session.WorkerSummaryInput{
		Summary:        "Created game.py",
		JobID:          jobID,
		AgentType:      orchestration.ProfileImplementer,
		ChildSessionID: child.ID,
		Status:         "complete",
		CompletedAt:    autoContinuePtrTime(time.Now().UTC()),
	}); err != nil {
		testutil.FailErr(t, "append implementer summary", err)
	}
	simulateWorkerJobComplete(t, mgr, q, jobID)

	testutil.WaitFor(t, 5*time.Second, func() bool {
		msgs, err := store.GetMessages(ctx, sess.ID)
		if err != nil {
			return false
		}
		assistants := 0
		hasAuto := false
		for _, msg := range msgs {
			if msg.Role == wire.MessageRoleAssistant {
				assistants++
			}
			if msg.Kind == wire.MessageKindHostLoopWake {
				hasAuto = true
			}
		}
		return assistants >= 1 && hasAuto
	})
}

func TestLegFinishedQueuesKickAndAutoPrompts(t *testing.T) {
	fix := setupLoopFixture(t, settings.DefaultSessionLimits())
	ctx := context.Background()

	if _, err := fix.mgr.Prompt(ctx, fix.sess.ID, "start implement"); err != nil {
		testutil.FailErr(t, "fix.mgr.Prompt failed", err)
	}
	child, err := fix.store.CreateChild(ctx, fix.sess, wire.SpawnChildRequest{AgentType: orchestration.ProfilePathExplorer})
	testutil.FailErr(t, "create child", err)
	if _, err := fix.mgr.AppendWorkerSummary(ctx, fix.sess.ID, session.WorkerSummaryInput{
		Summary:        "leg done",
		DelegationID:   "dep-1",
		LegID:          "leg-1",
		JobID:          "job-1",
		ChildSessionID: child.ID,
		AgentType:      orchestration.ProfilePathExplorer,
		Status:         "complete",
		CompletedAt:    autoContinuePtrTime(time.Now().UTC()),
	}); err != nil {
		testutil.FailErr(t, "append leg summary", err)
	}
	fix.mgr.NudgeCoordinatorLoop(ctx, fix.sess.ID, anchor.LegFinished, anchor.LegFinished, "leg-1", anchor.Envelope{
		CompletedAt: autoContinuePtrTime(time.Now().UTC()),
	})

	testutil.WaitFor(t, 5*time.Second, func() bool {
		msgs, err := fix.store.GetMessages(ctx, fix.sess.ID)
		if err != nil {
			return false
		}
		assistants := 0
		hasAuto := false
		for _, msg := range msgs {
			if msg.Role == wire.MessageRoleAssistant {
				assistants++
			}
			if msg.Kind == wire.MessageKindHostLoopWake {
				hasAuto = true
			}
		}
		return assistants >= 2 && hasAuto
	})
}

func autoContinuePtrTime(t time.Time) *time.Time {
	u := t.UTC()
	return &u
}

// simulateWorkerJobComplete applies the queue and session terminal hooks.
func simulateWorkerJobComplete(t *testing.T, mgr *session.Manager, q worker.WorkerQueue, jobID string) {
	t.Helper()
	ctx := context.Background()
	result := wire.WorkerResult{Status: "complete"}
	claimed, ok := q.Get(jobID)
	if !ok {
		t.Fatalf("worker job %s missing", jobID)
	}
	won, err := q.Complete(ctx, claimed, result)
	testutil.FailErr(t, "Complete", err)
	if !won {
		t.Fatal("completion claim lost")
	}
	bridge := &worker.SessionOutcomeBridge{Sessions: mgr}
	testutil.FailErr(t, "OnWorkerComplete", bridge.OnWorkerComplete(ctx, jobID, result))
	testutil.FailErr(t, "acknowledge worker outcome", q.MarkOutcomeDelivered(ctx, jobID))
}

func TestImplementWorkerSummaryPartialSkipsLoopWake(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "implement-autocontinue-partial.db")

	store := store.NewSQL(sqlDB)
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ack"}}}))
	mgr := session.NewManager(store, rec, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetOARPipeline(sessionTestOARPipeline(t), oar.NewRenderer(nil, nil))
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)
	mgr.SetAgentRegistry(agents)
	testutil.FailErr(t, "install anchor registry", mgr.InstallAnchorRegistry())

	wfStore := workflow.NewSQLStore(sqlDB)
	wfMgr := workflow.NewManager(wfStore, store, nil, nil)
	mgr.SetWorkflowSessionView(wfMgr, workflow.PolicySource(wfMgr))
	mgr.SetLoopWorkflowSource(wfMgr)
	mgr.SetWorkerQueue(worker.NewInMemoryQueue(4))

	ctx := context.Background()
	dir := t.TempDir()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	child, err := store.CreateChild(ctx, sess, wire.SpawnChildRequest{AgentType: orchestration.ProfileImplementer, Prompt: "touch game.py"})
	testutil.FailErr(t, "store.CreateChild failed", err)
	if err := store.AppendMessages(ctx, child.ID,
		wire.Message{
			Role: wire.MessageRoleAssistant,
			ToolCalls: []wire.ToolCall{
				{ID: "tc1", Name: "command", Args: map[string]any{"command": "echo hi > game.py"}},
			},
		},
		wire.Message{Role: wire.MessageRoleTool, Content: "ok"},
	); err != nil {
		testutil.FailErr(t, "store.AppendMessages failed", err)
	}

	status, err := mgr.AppendWorkerSummary(ctx, sess.ID, session.WorkerSummaryInput{
		Summary:        "Created game.py",
		JobID:          "job-command-only",
		AgentType:      orchestration.ProfileImplementer,
		ChildSessionID: child.ID,
		CompletedAt:    autoContinuePtrTime(time.Now().UTC()),
	})
	testutil.FailErr(t, "AppendWorkerSummary", err)
	if status != "partial" {
		t.Fatalf("status = %q want partial", status)
	}

	drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr.WaitForCoordinatorAsyncTurns(drainCtx)

	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	for _, msg := range msgs {
		if msg.Kind == wire.MessageKindHostLoopWake {
			t.Fatalf("partial worker summary must not loop-wake coordinator: %q", msg.Content)
		}
	}
}
