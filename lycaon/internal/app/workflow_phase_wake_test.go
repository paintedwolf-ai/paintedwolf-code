package app

import (
	"github.com/lycaon/lycaon/internal/app/delegations"
	"github.com/lycaon/lycaon/internal/app/sessions"
	"github.com/lycaon/lycaon/internal/app/workflows"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
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
	delegationsRt.SetDependencies(delegations.Dependencies{Workflows: &workflows.Runtime{Manager: wfMgr}, Sessions: &sessions.Runtime{Manager: sessionMgr}})
	finishExecution := sessionMgr.Coordinator.Runtime.CoordinatorLoop().Admission.BeginPromptExecution(t.Context(), sess.ID)
	defer finishExecution()
	delegationsRt.OnWorkflowPhaseAutoAdvanced(ctx, sess.ID, run.ID, "triage", "expand")

	got, ok := sessionMgr.Coordinator.Runtime.CoordinatorLoop().Nudges.Pending(sess.ID)
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
	kicks := &kick.KickEngine{}
	bus := anchor.NewBus(kicks)
	bus.SetRegistry(registry)
	host.Coordinator.Guidance.Bind(kicks, bus)
	runtime := delegations.New(sqlDB, nil, worker.WorkersConfig{})
	runtime.SetDependencies(delegations.Dependencies{Workflows: &workflows.Runtime{Manager: manager}, Sessions: &sessions.Runtime{Manager: host}})
	for _, tc := range []struct {
		version      string
		wantGuidance bool
	}{{"1.0.0", true}, {"9.9.9", false}} {
		t.Run(tc.version, func(t *testing.T) {
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
