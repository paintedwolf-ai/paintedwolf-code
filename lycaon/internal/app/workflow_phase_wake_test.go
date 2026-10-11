package app

import (
	"context"
	"github.com/lycaon/lycaon/internal/app/delegations"
	"github.com/lycaon/lycaon/internal/app/sessions"
	"github.com/lycaon/lycaon/internal/app/workflows"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/progress"
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
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	"github.com/lycaon/lycaon/pkg/api"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCrossPhaseHostAdvanceQueuesCoordinatorWake(t *testing.T) {
	ctx := t.Context()
	sqlDB := testdbfixture.OpenPath(t, t.TempDir()+"/phase-wake.db")

	sessionStore := store.NewSQL(sqlDB)
	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	sess, err := sessionStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark session busy", sessionStore.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))

	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "load manifests", err)
	wfStore := workflowpersistence.New(sqlDB)
	wfMgr := workflow.NewManager(wfStore, sessionStore, manifests, nil)
	now := time.Now().UTC()
	run := &api.WorkflowRun{
		ID: "bugbash-run", SessionID: sess.ID, ProjectID: testdbseed.DefaultProjectID,
		WorkflowID: "bugbash", WorkflowVersion: "1.0.0", Status: api.WorkflowRunStatusRunning,
		CurrentPhase: "expand", CreatedAt: now, UpdatedAt: now,
	}
	testutil.FailErr(t, "create workflow state", wfStore.State.CreateState(ctx, run, projectDir, nil))

	sessionMgr := session.NewHost(sessionStore, session.Models{Client: nil, Limits: settings.DefaultSessionLimits()}, tools.NewStubRegistry())
	sessionMgr.SetLoopWorkflowSource(&loopwake.WorkflowDomains{Runs: wfMgr.Store.Runs, Approvals: wfMgr.Policy, Obligations: wfMgr.Obligations})
	delegationsRt := delegations.New(sqlDB, nil, worker.WorkersConfig{})
	starts := 0
	delegationsRt.SetDependencies(delegations.Dependencies{Workflows: &workflows.Runtime{Manager: wfMgr}, Sessions: &sessions.Runtime{Manager: sessionMgr}, StartOrchestratedTopology: func(c context.Context, id string, started *api.WorkflowRun) {
		if c.Err() != nil || id != sess.ID || started.ID != run.ID {
			t.Fatalf("initial topology start crossed run identity: %s, %+v, %v", id, started, c.Err())
		}
		starts++
	}})
	finishExecution := sessionMgr.Coordinator.Runtime.CoordinatorLoop().Admission.BeginPromptExecution(t.Context(), sess.ID)
	defer finishExecution()
	delegationsRt.OnWorkflowPhaseAutoAdvanced(ctx, sess.ID, run.ID, "triage", "expand")

	nudges := sessionMgr.Coordinator.Runtime.CoordinatorLoop().Nudges
	nudges.ClearPending(sess.ID)
	delegationsRt.OnWorkflowPhaseAutoAdvanced(ctx, sess.ID, run.ID, "", "expand")
	if starts != 1 {
		t.Fatalf("initial phase topology starts=%d", starts)
	}
	nudges.ClearPending(sess.ID)
	delegationsRt.OnWorkflowRunResumed(ctx, run)
	if got, ok := nudges.Pending(sess.ID); !ok || got != anchor.PhaseAdvanced {
		t.Fatalf("resumed run wake=%s,%v", got, ok)
	}
	nudges.ClearPending(sess.ID)
	stale := *run
	stale.ID = "stale-run"
	delegationsRt.OnWorkflowRunResumed(ctx, &stale)
	if got, ok := nudges.Pending(sess.ID); ok {
		t.Fatalf("stale run resumed active coordinator: %s", got)
	}
	delegationsRt.OnWorkflowPhaseAutoAdvanced(ctx, sess.ID, run.ID, "triage", "expand")
	got, ok := nudges.Pending(sess.ID)
	if !ok || got != anchor.PhaseAdvanced {
		t.Fatalf("pending wake = %q, %v want %q, true", got, ok, anchor.PhaseAdvanced)
	}
}

func TestTerminalCompletionSettlesWithoutAmbientWake(t *testing.T) {
	ctx := t.Context()
	sqlDB := testdbfixture.OpenPath(t, t.TempDir()+"/terminal-wake.db")

	sessionStore := store.NewSQL(sqlDB)
	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	sess, err := sessionStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark parked session busy", sessionStore.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))

	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "load manifests", err)
	wfStore := workflowpersistence.New(sqlDB)
	wfMgr := workflow.NewManager(wfStore, sessionStore, manifests, nil)
	now := time.Now().UTC()
	run := &api.WorkflowRun{
		ID: "options-run", SessionID: sess.ID, ProjectID: testdbseed.DefaultProjectID,
		WorkflowID: "options", WorkflowVersion: "1.0.0", Status: api.WorkflowRunStatusComplete,
		CurrentPhase: "done", CreatedAt: now, UpdatedAt: now, CompletedAt: &now,
	}
	testutil.FailErr(t, "create workflow state", wfStore.State.CreateState(ctx, run, projectDir, nil))

	sessionMgr := session.NewHost(sessionStore, session.Models{Client: nil, Limits: settings.DefaultSessionLimits()}, tools.NewStubRegistry())
	sessionMgr.SetLoopWorkflowSource(&loopwake.WorkflowDomains{Runs: wfMgr.Store.Runs, Approvals: wfMgr.Policy, Obligations: wfMgr.Obligations})
	delegationsRt := delegations.New(sqlDB, nil, worker.WorkersConfig{})
	delegationsRt.SetDependencies(delegations.Dependencies{Workflows: &workflows.Runtime{Manager: wfMgr}, Sessions: &sessions.Runtime{Manager: sessionMgr}})
	delegationsRt.OnWorkflowPhaseAutoAdvanced(ctx, sess.ID, run.ID, "select", "done")
	delegationsRt.OnWorkflowRunCompleted(ctx, run)
	settled, err := sessionStore.Get(ctx, sess.ID)
	testutil.FailErr(t, "read completed session", err)
	if settled.Status != api.SessionStatusIdle {
		t.Fatalf("status = %q, want idle after workflow approval", settled.Status)
	}

	if got, ok := sessionMgr.Coordinator.Runtime.CoordinatorLoop().Nudges.Pending(sess.ID); ok {
		t.Fatalf("terminal workflow queued ambient wake %q", got)
	}
}

func TestHumanApprovalAdvanceQueuesWakeForRunningChild(t *testing.T) {
	ctx := t.Context()
	sqlDB := testdbfixture.OpenPath(t, t.TempDir()+"/approval-child-wake.db")

	sessionStore := store.NewSQL(sqlDB)
	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	sess, err := sessionStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark session busy", sessionStore.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))

	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "load manifests", err)
	wfStore := workflowpersistence.New(sqlDB)
	wfMgr := workflow.NewManager(wfStore, sessionStore, manifests, nil)
	now := time.Now().UTC()
	parent := &api.WorkflowRun{
		ID: "bugbash-parent", SessionID: sess.ID, ProjectID: testdbseed.DefaultProjectID,
		WorkflowID: "bugbash", WorkflowVersion: "1.0.0", Status: api.WorkflowRunStatusPausedOnChild,
		CurrentPhase: "execute", CreatedAt: now, UpdatedAt: now,
	}
	testutil.FailErr(t, "create parent workflow state", wfStore.State.CreateState(ctx, parent, projectDir, nil))
	parentID := parent.ID
	child := &api.WorkflowRun{
		ID: "implement-child", SessionID: sess.ID, ProjectID: testdbseed.DefaultProjectID,
		ParentRunID: &parentID, WorkflowID: "implement", WorkflowVersion: "1.0.0",
		Status: api.WorkflowRunStatusRunning, CurrentPhase: "boot", CreatedAt: now, UpdatedAt: now,
	}
	testutil.FailErr(t, "create child workflow state", wfStore.State.CreateState(ctx, child, projectDir, nil))

	sessionMgr := session.NewHost(sessionStore, session.Models{Client: nil, Limits: settings.DefaultSessionLimits()}, tools.NewStubRegistry())
	sessionMgr.SetLoopWorkflowSource(&loopwake.WorkflowDomains{Runs: wfMgr.Store.Runs, Approvals: wfMgr.Policy, Obligations: wfMgr.Obligations})
	delegationsChildRt := delegations.New(sqlDB, nil, worker.WorkersConfig{})
	delegationsChildRt.SetDependencies(delegations.Dependencies{Workflows: &workflows.Runtime{Manager: wfMgr}, Sessions: &sessions.Runtime{Manager: sessionMgr}})
	finishExecution := sessionMgr.Coordinator.Runtime.CoordinatorLoop().Admission.BeginPromptExecution(t.Context(), sess.ID)
	defer finishExecution()
	delegationsChildRt.OnWorkflowHumanApprovalAdvanced(ctx, parent)

	got, ok := sessionMgr.Coordinator.Runtime.CoordinatorLoop().Nudges.Pending(sess.ID)
	if !ok || got != anchor.PhaseAdvanced {
		t.Fatalf("pending child wake = %q, %v want %q, true", got, ok, anchor.PhaseAdvanced)
	}
}

func TestPhaseEntryGuidanceUsesTheRunWorkflowVersion(t *testing.T) {
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "phase-guidance.db")
	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	sessionsStore := store.NewSQL(sqlDB)
	sess, err := sessionsStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create phase guidance session", err)
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "load phase guidance workflows", err)
	manager := workflow.NewManager(workflowpersistence.New(sqlDB), sessionsStore, manifests, nil)
	host := session.NewHost(sessionsStore, session.Models{Limits: settings.DefaultSessionLimits()}, tools.NewStubRegistry())
	registry, err := anchor.LoadRegistryFromConfigRoot()
	testutil.FailErr(t, "load versioned phase guidance bindings", err)
	runtime := delegations.New(sqlDB, nil, worker.WorkersConfig{})
	runtime.SetDependencies(delegations.Dependencies{Workflows: &workflows.Runtime{Manager: manager}, Sessions: &sessions.Runtime{Manager: host}})
	for _, tc := range []struct {
		version      string
		wantGuidance bool
	}{{"1.0.0", true}, {"9.9.9", false}} {
		t.Run(tc.version, func(t *testing.T) {
			kicks := &kick.KickEngine{}
			bus := anchor.NewBus(kicks)
			bus.SetRegistry(registry)
			host.Coordinator.Guidance.Bind(kicks, bus)
			rc := &workflowphases.RunContext{SessionID: sess.ID, RunID: "phase-guidance-run", WorkflowID: "security-survey", WorkflowVersion: tc.version, Phase: "plan"}
			runtime.OnWorkflowPhaseEnter(ctx, rc, workflowdef.PhaseDef{ID: "plan"})
			got := kicks.TakePendingKickID(sess.ID)
			if tc.wantGuidance && got != "coordinator-security-plan" {
				t.Fatalf("versioned phase guidance = %q", got)
			}
			if !tc.wantGuidance && got != "" {
				t.Fatalf("unregistered workflow version queued %q", got)
			}
		})
	}
}

func TestPhaseHandoffCarriesDurableEvidenceAndResetsPreviousRunProgress(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "handoff-guidance.db")
	root := t.TempDir()
	testdbseed.InsertSessionWithRoot(t, database, "chat", testdbseed.DefaultProjectID, root)
	sessionsStore := store.NewSQL(database)
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "load handoff manifests", err)
	wfStore := workflowpersistence.New(database)
	manager := workflow.NewManager(wfStore, sessionsStore, manifests, nil)
	now := time.Now().UTC()
	run := &api.WorkflowRun{ID: "handoff", SessionID: "chat", ProjectID: testdbseed.DefaultProjectID, WorkflowID: "security-survey", WorkflowVersion: "1.0.0", Status: api.WorkflowRunStatusRunning, CurrentPhase: "claims", CreatedAt: now, UpdatedAt: now}
	vars := map[string]any{"evidence_digest": "retained-evidence-identity", "topology_outputs": map[string]any{"fan_out": "retained-topology-output"}, "options": map[string]any{"criterion": "explicit-selection-criterion"}, "review_verdict": map[string]any{"claim": "approved"}}
	testutil.FailErr(t, "create handoff state", wfStore.State.CreateState(ctx, run, root, vars))
	host := session.NewHost(sessionsStore, session.Models{Limits: settings.DefaultSessionLimits()}, tools.NewStubRegistry())
	kicks := &kick.KickEngine{}
	bus := anchor.NewBus(kicks)
	registry, err := anchor.LoadRegistryFromConfigRoot()
	testutil.FailErr(t, "load handoff guidance", err)
	bus.SetRegistry(registry)
	previousRegistry := anchor.DefaultRegistry()
	anchor.SetDefaultRegistry(registry)
	t.Cleanup(func() { anchor.SetDefaultRegistry(previousRegistry) })
	host.Coordinator.Guidance.Bind(kicks, bus)
	progressStore := progress.NewMemoryStore()
	progressStore.BindRun("chat", "previous-run")
	testutil.FailErr(t, "seed old progress", progressStore.Set("chat", "old run checklist"))
	runtime := delegations.New(database, nil, worker.WorkersConfig{})
	topologyCalls := 0
	runtime.SetDependencies(delegations.Dependencies{Sessions: &sessions.Runtime{Manager: host}, Workflows: &workflows.Runtime{Manager: manager}, Progress: func() progress.RunScopedStore { return progressStore }, StartOrchestratedTopology: func(_ context.Context, id string, snapshot *api.WorkflowRun) {
		topologyCalls++
		if id != "chat" || snapshot.ID != run.ID || snapshot.CurrentPhase != "claims" {
			t.Fatalf("topology handoff used wrong run:%q,%+v", id, snapshot)
		}
	}})
	runtime.OnWorkflowPhaseEnter(ctx, &workflowphases.RunContext{SessionID: "chat", RunID: run.ID, WorkflowID: run.WorkflowID, WorkflowVersion: run.WorkflowVersion, PreviousPhase: "execute", Phase: "claims"}, workflowdef.PhaseDef{ID: "claims", BindTopologyStage: "claims"})
	if topologyCalls != 1 || progressStore.BoundRunID("chat") != run.ID || strings.Contains(progressStore.Get(ctx, "chat"), "old run checklist") {
		t.Fatal("phase handoff retained previous run progress or missed topology")
	}
	kicks.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()}))
	if got := kicks.TakePendingKickID("chat"); got != "coordinator-security-claims" {
		t.Fatalf("phase handoff guidance=%q", got)
	}
	text, _, ok, err := kicks.RenderPendingNudge(ctx, "chat", kick.CoordinatorKickRenderContext{WorkflowID: run.WorkflowID, CurrentPhase: run.CurrentPhase})
	testutil.FailErr(t, "render durable phase evidence", err)
	if !ok || !strings.Contains(text, "retained-evidence-identity") {
		t.Fatalf("phase handoff lost retained evidence:%q,%v", text, ok)
	}
}

func TestFeedbackAndReviewCallbacksRetainGuidanceUntilCoordinatorWake(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "feedback-guidance.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	sessionStore := store.NewSQL(database)
	sess, err := sessionStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create coordinator feedback session", err)
	host := session.NewHost(sessionStore, session.Models{Limits: settings.DefaultSessionLimits()}, tools.NewStubRegistry())
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "load feedback workflow catalog", err)
	manager := workflow.NewManager(workflowpersistence.New(database), sessionStore, manifests, nil)
	host.SetLoopWorkflowSource(&loopwake.WorkflowDomains{Runs: manager.Store.Runs, Approvals: manager.Policy, Obligations: manager.Obligations})
	kicks := &kick.KickEngine{}
	bus := anchor.NewBus(kicks)
	registry, err := anchor.LoadRegistryFromConfigRoot()
	testutil.FailErr(t, "load feedback guidance", err)
	bus.SetRegistry(registry)
	previousRegistry := anchor.DefaultRegistry()
	anchor.SetDefaultRegistry(registry)
	t.Cleanup(func() { anchor.SetDefaultRegistry(previousRegistry) })
	host.Coordinator.Guidance.Bind(kicks, bus)
	runtime := delegations.New(database, nil, worker.WorkersConfig{})
	runtime.SetDependencies(delegations.Dependencies{Sessions: &sessions.Runtime{Manager: host}})
	finish := host.Coordinator.Runtime.CoordinatorLoop().Admission.BeginPromptExecution(ctx, sess.ID)
	defer finish()
	runtime.OnWorkflowFeedbackPending(ctx, sess.ID, "run")
	if ids := kicks.PendingKickIDsUnless(sess.ID, nil); !slices.Contains(ids, anchor.InformRender(anchor.FeedbackPending)) {
		t.Fatalf("feedback missing pending guidance:%v", ids)
	}
	runtime.OnWorkflowFeedbackResolved(ctx, sess.ID, "run", "prompt", "answer")
	ids := kicks.PendingKickIDsUnless(sess.ID, nil)
	if slices.Contains(ids, anchor.InformRender(anchor.FeedbackPending)) || !slices.Contains(ids, anchor.InformRender(anchor.FeedbackReceived)) {
		t.Fatalf("resolved feedback left stale guidance:%v", ids)
	}
	for _, decision := range []bool{false, true} {
		runtime.OnWorkflowReviewLoopHeld(ctx, sess.ID, decision)
		want := anchor.ReviewLoopContinue
		if decision {
			want = anchor.ReviewLoopDecide
		}
		if ids := kicks.PendingKickIDsUnless(sess.ID, nil); !slices.Contains(ids, anchor.InformRender(want)) {
			t.Fatalf("review decision %v missing guidance:%v", decision, ids)
		}
	}
	if id, ok := host.Coordinator.Runtime.CoordinatorLoop().Nudges.Pending(sess.ID); !ok || id != anchor.PhaseAdvanced {
		t.Fatalf("feedback/review callbacks did not wake coordinator:%q,%v", id, ok)
	}
}
