package app

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
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
	wfStore := workflow.NewSQLStore(sqlDB)
	wfMgr := workflow.NewManager(wfStore, sessionStore, manifests, nil)
	wfMgr.Resolver = workflow.ManifestResolver{}
	now := time.Now().UTC()
	run := &api.WorkflowRun{
		ID: "bugbash-run", SessionID: sess.ID, ProjectID: testdbseed.DefaultProjectID,
		WorkflowID: "bugbash", WorkflowVersion: "1.0.0", Status: api.WorkflowRunStatusRunning,
		CurrentPhase: "expand", CreatedAt: now, UpdatedAt: now,
	}
	testutil.FailErr(t, "create workflow state", wfStore.CreateState(ctx, run, projectDir, nil))

	sessionMgr := session.NewManager(sessionStore, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	sessionMgr.SetLoopWorkflowSource(&loopwake.WorkflowDomains{Runs: wfMgr.Store.Runs, Approvals: wfMgr.Policy, Obligations: wfMgr.Obligations})
	b := &serveBuilder{mgr: sessionMgr, workflowMgr: wfMgr}
	finishExecution := sessionMgr.Runner.Coordinator.CoordinatorLoop().BeginPromptExecution(t.Context(), sess.ID)
	defer finishExecution()
	delegationWiring{b}.onWorkflowPhaseAutoAdvanced(ctx, sess.ID, run.ID, "triage", "expand")

	got, ok := sessionMgr.Runner.Coordinator.CoordinatorLoop().PendingForTest(sess.ID)
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
	wfStore := workflow.NewSQLStore(sqlDB)
	wfMgr := workflow.NewManager(wfStore, sessionStore, manifests, nil)
	now := time.Now().UTC()
	run := &api.WorkflowRun{
		ID: "options-run", SessionID: sess.ID, ProjectID: testdbseed.DefaultProjectID,
		WorkflowID: "options", WorkflowVersion: "1.0.0", Status: api.WorkflowRunStatusComplete,
		CurrentPhase: "done", CreatedAt: now, UpdatedAt: now, CompletedAt: &now,
	}
	testutil.FailErr(t, "create workflow state", wfStore.CreateState(ctx, run, projectDir, nil))

	sessionMgr := session.NewManager(sessionStore, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	sessionMgr.SetLoopWorkflowSource(&loopwake.WorkflowDomains{Runs: wfMgr.Store.Runs, Approvals: wfMgr.Policy, Obligations: wfMgr.Obligations})
	b := &serveBuilder{mgr: sessionMgr, workflowMgr: wfMgr}
	delegationWiring{b}.onWorkflowPhaseAutoAdvanced(ctx, sess.ID, run.ID, "select", "done")
	delegationWiring{b}.onWorkflowRunCompleted(ctx, run)
	settled, err := sessionStore.Get(ctx, sess.ID)
	testutil.FailErr(t, "read completed session", err)
	if settled.Status != api.SessionStatusIdle {
		t.Fatalf("status = %q, want idle after workflow approval", settled.Status)
	}

	if got, ok := sessionMgr.Runner.Coordinator.CoordinatorLoop().PendingForTest(sess.ID); ok {
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
	wfStore := workflow.NewSQLStore(sqlDB)
	wfMgr := workflow.NewManager(wfStore, sessionStore, manifests, nil)
	now := time.Now().UTC()
	parent := &api.WorkflowRun{
		ID: "bugbash-parent", SessionID: sess.ID, ProjectID: testdbseed.DefaultProjectID,
		WorkflowID: "bugbash", WorkflowVersion: "1.0.0", Status: api.WorkflowRunStatusPausedOnChild,
		CurrentPhase: "execute", CreatedAt: now, UpdatedAt: now,
	}
	testutil.FailErr(t, "create parent workflow state", wfStore.CreateState(ctx, parent, projectDir, nil))
	parentID := parent.ID
	child := &api.WorkflowRun{
		ID: "implement-child", SessionID: sess.ID, ProjectID: testdbseed.DefaultProjectID,
		ParentRunID: &parentID, WorkflowID: "implement", WorkflowVersion: "1.0.0",
		Status: api.WorkflowRunStatusRunning, CurrentPhase: "boot", CreatedAt: now, UpdatedAt: now,
	}
	testutil.FailErr(t, "create child workflow state", wfStore.CreateState(ctx, child, projectDir, nil))

	sessionMgr := session.NewManager(sessionStore, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	sessionMgr.SetLoopWorkflowSource(&loopwake.WorkflowDomains{Runs: wfMgr.Store.Runs, Approvals: wfMgr.Policy, Obligations: wfMgr.Obligations})
	finishExecution := sessionMgr.Runner.Coordinator.CoordinatorLoop().BeginPromptExecution(t.Context(), sess.ID)
	defer finishExecution()
	b := &serveBuilder{mgr: sessionMgr, workflowMgr: wfMgr}
	delegationWiring{b}.onWorkflowHumanApprovalAdvanced(ctx, parent)

	got, ok := sessionMgr.Runner.Coordinator.CoordinatorLoop().PendingForTest(sess.ID)
	if !ok || got != anchor.PhaseAdvanced {
		t.Fatalf("pending child wake = %q, %v want %q, true", got, ok, anchor.PhaseAdvanced)
	}
}
