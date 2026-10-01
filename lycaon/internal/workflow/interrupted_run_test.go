package workflow

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/extpacks"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReconcileOrphanedRunEmitsInterruptedBoundary(t *testing.T) {
	mgr, store, _, projectDir := testManager(t)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	mgr.SessionCoordinatorBusy = func(context.Context, string) bool { return false }

	run := &api.WorkflowRun{
		ID: "run-int", SessionID: sess.ID, WorkflowID: "plan",
		WorkflowVersion: "1.0.0", Status: api.WorkflowRunStatusRunning, CurrentPhase: "boot",
	}
	testutil.FailErr(t, "create run", mgr.Store.CreateState(ctx, run, projectDir, nil))
	testutil.FailErr(t, "reconcile", mgr.ReconcileOrphanedRuns(ctx, sess.ID))

	got, err := mgr.Store.Get(ctx, run.ID)
	testutil.FailErr(t, "get run", err)
	if got.Status != api.WorkflowRunStatusInterrupted {
		t.Fatalf("status = %q want interrupted", got.Status)
	}
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get messages", err)
	for _, msg := range msgs {
		if msg.WorkflowBoundary != nil && msg.WorkflowBoundary.Event == "interrupted" {
			return
		}
	}
	t.Fatal("expected interrupted boundary row")
}

// Ambient implement@ survives the post-create reconcile, so the first user
// prompt is stamped with its workflow_run_id.
func TestReconcileOrphanedRunsSkipsAmbientSessionCreate(t *testing.T) {
	mgr, store, _, _ := testManager(t)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	mgr.SessionCoordinatorBusy = func(context.Context, string) bool { return false }

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	bundledDir := filepath.Join(filepath.Dir(file), "..", "..", "config", "packs", "painted-wolf", "platform", "workflows")
	ref, err := workflowdef.LoadRegistryConfig(extpacks.OnDisk(bundledDir))
	testutil.FailErr(t, "LoadRegistryConfig", err)
	ambient, err := mgr.StartAmbient(ctx, sess.ID, ref.ID, ref.Version)
	testutil.FailErr(t, "StartAmbient", err)

	testutil.FailErr(t, "reconcile", mgr.ReconcileOrphanedRuns(ctx, sess.ID))

	got, err := mgr.Store.Get(ctx, ambient.ID)
	testutil.FailErr(t, "get ambient", err)
	if got.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("ambient status = %q want running (must not interrupt session_create attach)", got.Status)
	}
	active, err := mgr.Store.ActiveBySession(ctx, sess.ID)
	testutil.FailErr(t, "ActiveBySession", err)
	if active == nil || active.ID != ambient.ID {
		t.Fatalf("active run = %v want ambient %q", active, ambient.ID)
	}

	testutil.FailErr(t, "StampAndAppendMessages", mgr.StampAndAppendMessages(ctx, sess.ID, api.Message{
		Role:    api.MessageRoleUser,
		Content: "Tell me about this repo",
	}))
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleUser && msg.Content == "Tell me about this repo" {
			if msg.WorkflowRunID != ambient.ID {
				t.Fatalf("user workflow_run_id = %q want ambient %q", msg.WorkflowRunID, ambient.ID)
			}
			return
		}
	}
	t.Fatal("expected stamped user message")
}

// Child implement@ shares attach policy session_create but is not the ambient
// root, so orphan reconcile interrupts it.
func TestReconcileOrphanedRunsInterruptsChildImplement(t *testing.T) {
	mgr, store, _, _ := testManager(t)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	mgr.SessionCoordinatorBusy = func(context.Context, string) bool { return false }

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	bundledDir := filepath.Join(filepath.Dir(file), "..", "..", "config", "packs", "painted-wolf", "platform", "workflows")
	ref, err := workflowdef.LoadRegistryConfig(extpacks.OnDisk(bundledDir))
	testutil.FailErr(t, "LoadRegistryConfig", err)
	ambient, err := mgr.StartAmbient(ctx, sess.ID, ref.ID, ref.Version)
	testutil.FailErr(t, "StartAmbient", err)

	// An orphaned child under a paused parent, without the full invoke path.
	now := ambient.UpdatedAt
	ambient.Status = api.WorkflowRunStatusPausedOnChild
	ambient.CompletedAt = nil
	ambient.UpdatedAt = now
	testutil.FailErr(t, "pause ambient parent", mgr.Store.Update(ctx, ambient))

	parentID := ambient.ID
	child := &api.WorkflowRun{
		ID:              "run-child-impl",
		SessionID:       sess.ID,
		WorkflowID:      ref.ID,
		WorkflowVersion: ref.Version,
		AttachPolicy:    string(workflowdef.AttachPolicySessionCreate),
		ParentRunID:     &parentID,
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "boot",
	}
	testutil.FailErr(t, "create child", mgr.Store.CreateState(ctx, child, sess.WorkspacePath, nil))
	testutil.FailErr(t, "reconcile", mgr.ReconcileOrphanedRuns(ctx, sess.ID))

	gotChild, err := mgr.Store.Get(ctx, child.ID)
	testutil.FailErr(t, "get child", err)
	if gotChild.Status != api.WorkflowRunStatusInterrupted {
		t.Fatalf("child status = %q want interrupted", gotChild.Status)
	}
	gotAmbient, err := mgr.Store.Get(ctx, ambient.ID)
	testutil.FailErr(t, "get ambient", err)
	if gotAmbient.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("ambient parent status = %q want running after interrupted child", gotAmbient.Status)
	}
	vars, err := mgr.Store.GetScaffoldVars(ctx, ambient.ID)
	testutil.FailErr(t, "load resumed parent vars", err)
	childRun, _ := vars["child_run"].(map[string]any)
	if childRun["status"] != string(api.WorkflowRunStatusInterrupted) {
		t.Fatalf("child_run vars = %+v want interrupted", childRun)
	}
}

// A pre-boot run whose session has run turns under this process is not an orphan:
// a turn that did work since boot adopted it, however long the run idles
// between wakes.
func TestReconcileOrphanedRunsSkipsSessionsActiveSinceBoot(t *testing.T) {
	mgr, store, _, _ := testManager(t)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	mgr.SessionCoordinatorBusy = func(context.Context, string) bool { return false }

	run := &api.WorkflowRun{
		ID: "run-resumed", SessionID: sess.ID, WorkflowID: "plan",
		WorkflowVersion: "1.0.0", Status: api.WorkflowRunStatusRunning, CurrentPhase: "review",
	}
	testutil.FailErr(t, "create run", mgr.Store.CreateState(ctx, run, sess.WorkspacePath, nil))

	// Simulated restart: the run predates the boot cutoff.
	mgr.OrphanReconcileBefore = time.Now().UTC()

	// Resumed activity under this process: a turn starts and finishes.
	time.Sleep(2 * time.Millisecond)
	execution, err := store.BeginTurn(ctx, sessionstore.TurnStart{
		SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: sessionstore.TurnOriginUser, InputJSON: "{}",
	})
	testutil.FailErr(t, "begin turn", err)
	_, err = store.FinishTurn(ctx, execution.Turn.ID, execution.Attempt.ID, sessionstore.TurnStatusComplete, "", "", "")
	testutil.FailErr(t, "finish turn", err)

	testutil.FailErr(t, "reconcile", mgr.ReconcileOrphanedRuns(ctx, sess.ID))
	got, err := mgr.Store.Get(ctx, run.ID)
	testutil.FailErr(t, "get run", err)
	if got.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("status = %q want running (resumed session must not be reconciled)", got.Status)
	}
}

// Boot recovery idles a session its dead turn left busy, and the person may
// rename or pin the chat before opening it. None of that is work: the pre-boot
// run is still an orphan.
func TestReconcileOrphanedRunsInterruptsRunsOfSessionsOnlyTouchedSinceBoot(t *testing.T) {
	mgr, store, _, _ := testManager(t)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	mgr.SessionCoordinatorBusy = func(context.Context, string) bool { return false }
	testutil.FailErr(t, "SetSessionStatus busy", store.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))

	run := &api.WorkflowRun{
		ID: "run-crashed", SessionID: sess.ID, WorkflowID: "plan",
		WorkflowVersion: "1.0.0", Status: api.WorkflowRunStatusRunning, CurrentPhase: "review",
	}
	testutil.FailErr(t, "create run", mgr.Store.CreateState(ctx, run, sess.WorkspacePath, nil))

	time.Sleep(2 * time.Millisecond)
	mgr.OrphanReconcileBefore = time.Now().UTC()
	time.Sleep(2 * time.Millisecond)
	testutil.FailErr(t, "SetSessionStatus idle", store.SetSessionStatus(ctx, sess.ID, api.SessionStatusIdle))
	testutil.FailErr(t, "rename", store.UpdateSession(ctx, sess.ID, func(s *api.Session) {
		s.Title = "Renamed after restart"
	}))

	testutil.FailErr(t, "reconcile", mgr.ReconcileOrphanedRuns(ctx, sess.ID))
	got, err := mgr.Store.Get(ctx, run.ID)
	testutil.FailErr(t, "get run", err)
	if got.Status != api.WorkflowRunStatusInterrupted {
		t.Fatalf("status = %q want interrupted (recovery and renames are not work)", got.Status)
	}
}

// The inverse control: a pre-boot run whose session has done no work since the
// boot cutoff is a genuine orphan and still interrupts.
func TestReconcileOrphanedRunsInterruptsUntouchedPreBootRuns(t *testing.T) {
	mgr, store, _, _ := testManager(t)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	mgr.SessionCoordinatorBusy = func(context.Context, string) bool { return false }

	run := &api.WorkflowRun{
		ID: "run-dead", SessionID: sess.ID, WorkflowID: "plan",
		WorkflowVersion: "1.0.0", Status: api.WorkflowRunStatusRunning, CurrentPhase: "review",
	}
	testutil.FailErr(t, "create run", mgr.Store.CreateState(ctx, run, sess.WorkspacePath, nil))

	time.Sleep(2 * time.Millisecond)
	mgr.OrphanReconcileBefore = time.Now().UTC()

	testutil.FailErr(t, "reconcile", mgr.ReconcileOrphanedRuns(ctx, sess.ID))
	got, err := mgr.Store.Get(ctx, run.ID)
	testutil.FailErr(t, "get run", err)
	if got.Status != api.WorkflowRunStatusInterrupted {
		t.Fatalf("status = %q want interrupted (untouched pre-boot run is an orphan)", got.Status)
	}
}

func TestReconcileOrphanedRunsPreservesPendingUserInput(t *testing.T) {
	mgr, store, _, projectDir := testManager(t)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	mgr.SessionCoordinatorBusy = func(context.Context, string) bool { return false }

	run := &api.WorkflowRun{
		ID: "run-awaiting-user", SessionID: sess.ID, WorkflowID: "plan",
		WorkflowVersion: "1.0.0", Status: api.WorkflowRunStatusRunning, CurrentPhase: "review",
	}
	testutil.FailErr(t, "create run", mgr.Store.CreateState(ctx, run, projectDir, nil))
	testutil.FailErr(t, "persist pending user input", mgr.Store.UpdateVars(
		ctx,
		run,
		projectDir,
		map[string]any{
			"user_feedback": map[string]any{
				"review": map[string]any{"pending": true, "prompt": "Approve this?"},
			},
		},
	))

	// Model the next sidecar boot: the run and session both predate its cutoff.
	time.Sleep(2 * time.Millisecond)
	mgr.OrphanReconcileBefore = time.Now().UTC()

	testutil.FailErr(t, "reconcile", mgr.ReconcileOrphanedRuns(ctx, sess.ID))
	got, err := mgr.Store.Get(ctx, run.ID)
	testutil.FailErr(t, "get run", err)
	if got.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("status = %q want running while user input is pending", got.Status)
	}
}

func TestReconcileOrphanedRunsPreservesHumanApproval(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := mgr.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0", Request: "test request",
	})
	testutil.FailErr(t, "start plan", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "complete research at depth none", err)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	run, err = advancePlanThroughExpand(ctx, mgr, run)
	testutil.FailErr(t, "advance through expand", err)
	run, err = completePlanReviewAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "skip review", err)
	if run.CurrentPhase != "approve" {
		t.Fatalf("phase = %q want approve", run.CurrentPhase)
	}
	mgr.SessionCoordinatorBusy = func(context.Context, string) bool { return false }

	// Model the next sidecar boot after the approval bar was already present.
	time.Sleep(2 * time.Millisecond)
	mgr.OrphanReconcileBefore = time.Now().UTC()
	testutil.FailErr(t, "reconcile", mgr.ReconcileOrphanedRuns(ctx, run.SessionID))
	got, err := mgr.Store.Get(ctx, run.ID)
	testutil.FailErr(t, "get run", err)
	if got.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("status = %q want running while approval is pending", got.Status)
	}
}
