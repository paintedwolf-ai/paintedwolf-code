package wiring

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkflowRetiredRefusal_StartHumanRefusesRetiredVersion(t *testing.T) {
	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := context.Background()
	dir := h.ProjectDir(t, "security-survey")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, dir)
	testutil.FailErr(t, "create session", err)

	// Attempting to start retired 1.0.0 must fail
	_, err = h.WorkflowMgr.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID:      "security-survey",
		WorkflowVersion: "1.0.0",
	})
	if err == nil {
		t.Fatal("expected StartHuman for retired 1.0.0 to fail, got nil error")
	}
	if !errors.Is(err, workflowdef.ErrUnknownWorkflow) {
		t.Fatalf("expected ErrUnknownWorkflow for retired 1.0.0, got %v", err)
	}

	// Starting active 2.0.0 must succeed
	run200, err := h.WorkflowMgr.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID:      "security-survey",
		WorkflowVersion: "2.0.0",
	})
	testutil.FailErr(t, "start security-survey 2.0.0", err)
	if run200 == nil || run200.WorkflowVersion != "2.0.0" {
		t.Fatalf("expected run with version 2.0.0, got %+v", run200)
	}
}

func TestWorkflowRetiredRefusal_AdvanceRefusesMissingVersion(t *testing.T) {
	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := context.Background()
	dir := h.ProjectDir(t, "security-survey")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, dir)
	testutil.FailErr(t, "create session", err)

	// No catalog defines the version this run is pinned to.
	// Seed a run in the store for this missing-version workflow
	now := time.Now().UTC()
	runID := "run-" + uuid.NewString()
	run := &api.WorkflowRun{
		ID:              runID,
		SessionID:       sess.ID,
		WorkflowID:      "missing-version-workflow",
		WorkflowVersion: "1.0.0",
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "intake",
		CreatedAt:       now,
		UpdatedAt:       now,
		Revision:        1,
	}
	err = h.WorkflowMgr.Store.CreateState(ctx, run, dir, map[string]any{})
	testutil.FailErr(t, "create state for missing-version run", err)

	// Advance must fail with WorkflowVersionUnavailableError
	_, err = h.WorkflowMgr.Advance(ctx, runID)
	if err == nil {
		t.Fatal("expected advance to fail for missing-version workflow, got nil error")
	}
	var unavailErr *workflow.WorkflowVersionUnavailableError
	if !errors.As(err, &unavailErr) {
		t.Fatalf("expected WorkflowVersionUnavailableError, got %T: %v", err, err)
	}
	if unavailErr.RejectionCode() != "WORKFLOW_VERSION_UNAVAILABLE" {
		t.Fatalf("expected code WORKFLOW_VERSION_UNAVAILABLE, got %q", unavailErr.RejectionCode())
	}
}

func TestWorkflowRetiredRefusal_ResumeRefusesMissingVersion(t *testing.T) {
	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := context.Background()
	dir := h.ProjectDir(t, "security-survey-resume")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, dir)
	testutil.FailErr(t, "create session", err)

	// No catalog defines the version this run is pinned to.
	now := time.Now().UTC()
	runID := "run-" + uuid.NewString()
	pausedRun := &api.WorkflowRun{
		ID:              runID,
		SessionID:       sess.ID,
		WorkflowID:      "missing-version-resume-test",
		WorkflowVersion: "1.0.0",
		Status:          api.WorkflowRunStatusPaused,
		PauseReason:     "testing",
		PausedAt:        &now,
		CurrentPhase:    "intake",
		CreatedAt:       now,
		UpdatedAt:       now,
		Revision:        1,
	}
	err = h.WorkflowMgr.Store.CreateState(ctx, pausedRun, dir, map[string]any{})
	testutil.FailErr(t, "create state for paused missing-version run", err)

	// Resume must fail early without flipping run to Running
	_, err = h.WorkflowMgr.Resume(ctx, runID)
	if err == nil {
		t.Fatal("expected Resume to fail for missing-version workflow, got nil error")
	}
	var unavailErr *workflow.WorkflowVersionUnavailableError
	if !errors.As(err, &unavailErr) {
		t.Fatalf("expected WorkflowVersionUnavailableError, got %T: %v", err, err)
	}
	if unavailErr.RejectionCode() != "WORKFLOW_VERSION_UNAVAILABLE" {
		t.Fatalf("expected code WORKFLOW_VERSION_UNAVAILABLE, got %q", unavailErr.RejectionCode())
	}

	persisted, err := h.WorkflowMgr.Store.Get(ctx, runID)
	testutil.FailErr(t, "get run after failed resume", err)
	if persisted.Status != api.WorkflowRunStatusPaused {
		t.Fatalf("expected run to remain paused, got status %v", persisted.Status)
	}
}

func TestWorkflowRetiredRefusal_ResolveFeedbackAndDecisionRefuseMissingVersion(t *testing.T) {
	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := people.WithCaller(context.Background(), people.Person{ID: "tester-1", Role: api.PersonRoleOwner})
	dir := h.ProjectDir(t, "security-survey-missing")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, dir)
	testutil.FailErr(t, "create session", err)

	// No catalog defines the version this run is pinned to.
	now := time.Now().UTC()
	runID := "run-" + uuid.NewString()
	run := &api.WorkflowRun{
		ID:              runID,
		SessionID:       sess.ID,
		WorkflowID:      "missing-version-feedback-test",
		WorkflowVersion: "1.0.0",
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "intake",
		CreatedAt:       now,
		UpdatedAt:       now,
		Revision:        1,
	}
	err = h.WorkflowMgr.Store.CreateState(ctx, run, dir, map[string]any{})
	testutil.FailErr(t, "create state for missing-version run", err)

	// ResolveUserFeedback must reject missing-version workflow
	_, err = h.WorkflowMgr.ResolveUserFeedback(ctx, sess.ID, runID, "intake", "user reply")
	if err == nil {
		t.Fatal("expected ResolveUserFeedback to fail for missing-version workflow, got nil error")
	}
	var unavailErr *workflow.WorkflowVersionUnavailableError
	if !errors.As(err, &unavailErr) {
		t.Fatalf("expected WorkflowVersionUnavailableError for ResolveUserFeedback, got %T: %v", err, err)
	}

	// ResolveUserDecision must reject missing-version workflow
	_, err = h.WorkflowMgr.ResolveUserDecision(ctx, sess.ID, runID, "intake", []string{"choice1"}, "")
	if err == nil {
		t.Fatal("expected ResolveUserDecision to fail for missing-version workflow, got nil error")
	}
	if !errors.As(err, &unavailErr) {
		t.Fatalf("expected WorkflowVersionUnavailableError for ResolveUserDecision, got %T: %v", err, err)
	}
}

func TestWorkflowRetiredRefusal_AutoAdvanceRefusesMissingVersion(t *testing.T) {
	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := context.Background()
	dir := h.ProjectDir(t, "security-survey-autoadvance")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, dir)
	testutil.FailErr(t, "create session", err)

	// No catalog defines the version this run is pinned to.
	now := time.Now().UTC()
	runID := "run-" + uuid.NewString()
	run := &api.WorkflowRun{
		ID:              runID,
		SessionID:       sess.ID,
		WorkflowID:      "missing-version-autoadvance-test",
		WorkflowVersion: "1.0.0",
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "intake",
		CreatedAt:       now,
		UpdatedAt:       now,
		Revision:        1,
	}
	err = h.WorkflowMgr.Store.CreateState(ctx, run, dir, map[string]any{})
	testutil.FailErr(t, "create state for missing-version run", err)

	_, err = h.WorkflowMgr.TryAutoAdvance(ctx, runID)
	if err == nil {
		t.Fatal("expected TryAutoAdvance to fail for missing-version workflow, got nil error")
	}
	var unavailErr *workflow.WorkflowVersionUnavailableError
	if !errors.As(err, &unavailErr) {
		t.Fatalf("expected WorkflowVersionUnavailableError for TryAutoAdvance, got %T: %v", err, err)
	}
	if unavailErr.RejectionCode() != "WORKFLOW_VERSION_UNAVAILABLE" {
		t.Fatalf("expected code WORKFLOW_VERSION_UNAVAILABLE, got %q", unavailErr.RejectionCode())
	}
}

func TestWorkflowRetiredRefusal_AssertRunnableRefusesMissingVersion(t *testing.T) {
	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := context.Background()
	dir := h.ProjectDir(t, "security-survey-assert-runnable")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, dir)
	testutil.FailErr(t, "create session", err)

	// No catalog defines the version this run is pinned to.
	now := time.Now().UTC()
	runID := "run-" + uuid.NewString()
	run := &api.WorkflowRun{
		ID:              runID,
		SessionID:       sess.ID,
		WorkflowID:      "missing-version-assert-runnable-test",
		WorkflowVersion: "1.0.0",
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "intake",
		CreatedAt:       now,
		UpdatedAt:       now,
		Revision:        1,
	}
	err = h.WorkflowMgr.Store.CreateState(ctx, run, dir, map[string]any{})
	testutil.FailErr(t, "create state for missing-version run", err)

	err = h.WorkflowMgr.AssertRunnable(ctx, runID)
	if err == nil {
		t.Fatal("expected AssertRunnable to fail for missing-version workflow, got nil error")
	}
	var unavailErr *workflow.WorkflowVersionUnavailableError
	if !errors.As(err, &unavailErr) {
		t.Fatalf("expected WorkflowVersionUnavailableError for AssertRunnable, got %T: %v", err, err)
	}

	err = h.WorkflowMgr.AssertSessionRunnable(ctx, sess.ID)
	if err == nil {
		t.Fatal("expected AssertSessionRunnable to fail for missing-version workflow, got nil error")
	}
	if !errors.As(err, &unavailErr) {
		t.Fatalf("expected WorkflowVersionUnavailableError for AssertSessionRunnable, got %T: %v", err, err)
	}
}
