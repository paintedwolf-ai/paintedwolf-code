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

func TestWorkflowRetiredRefusal_AdvanceRefusesUnsealedRetiredVersion(t *testing.T) {
	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := context.Background()
	dir := h.ProjectDir(t, "security-survey")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, dir)
	testutil.FailErr(t, "create session", err)

	// Register a retired workflow with no sealed archive copy: no existing run
	// may resume it, so Advance must refuse with WORKFLOW_VERSION_UNAVAILABLE.
	entries := h.WorkflowMgr.Manifests.All()
	unsealedRetired := workflowdef.Manifest{
		ID:      "unsealed-retired-workflow",
		Version: "1.0.0",
		Name:    "Unsealed retired test",
		Retired: true,
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "intake", Next: "review"},
			{ID: "review", Terminal: true},
		},
	}
	entries[workflowdef.ManifestKey(unsealedRetired.ID, unsealedRetired.Version)] = unsealedRetired
	h.WorkflowMgr.Manifests = workflowdef.NewRegistry(entries)

	// Seed a run in the store for this unsealed retired workflow
	now := time.Now().UTC()
	runID := "run-" + uuid.NewString()
	run := &api.WorkflowRun{
		ID:              runID,
		SessionID:       sess.ID,
		WorkflowID:      "unsealed-retired-workflow",
		WorkflowVersion: "1.0.0",
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "intake",
		CreatedAt:       now,
		UpdatedAt:       now,
		Revision:        1,
	}
	err = h.WorkflowMgr.Store.CreateState(ctx, run, dir, map[string]any{})
	testutil.FailErr(t, "create state for unsealed retired run", err)

	// Advance must fail with WorkflowVersionUnavailableError
	_, err = h.WorkflowMgr.Advance(ctx, runID)
	if err == nil {
		t.Fatal("expected advance to fail for unsealed retired workflow, got nil error")
	}
	var unavailErr *workflow.WorkflowVersionUnavailableError
	if !errors.As(err, &unavailErr) {
		t.Fatalf("expected WorkflowVersionUnavailableError, got %T: %v", err, err)
	}
	if unavailErr.RejectionCode() != "WORKFLOW_VERSION_UNAVAILABLE" {
		t.Fatalf("expected code WORKFLOW_VERSION_UNAVAILABLE, got %q", unavailErr.RejectionCode())
	}
}

func TestWorkflowRetiredRefusal_ResumeRefusesUnsealedRetiredVersion(t *testing.T) {
	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := context.Background()
	dir := h.ProjectDir(t, "security-survey-resume")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, dir)
	testutil.FailErr(t, "create session", err)

	entries := h.WorkflowMgr.Manifests.All()
	unsealedRetired := workflowdef.Manifest{
		ID:      "unsealed-retired-resume-test",
		Version: "1.0.0",
		Name:    "Unsealed retired resume test",
		Retired: true,
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "intake", Next: "review"},
			{ID: "review", Terminal: true},
		},
	}
	entries[workflowdef.ManifestKey(unsealedRetired.ID, unsealedRetired.Version)] = unsealedRetired
	h.WorkflowMgr.Manifests = workflowdef.NewRegistry(entries)

	now := time.Now().UTC()
	runID := "run-" + uuid.NewString()
	pausedRun := &api.WorkflowRun{
		ID:              runID,
		SessionID:       sess.ID,
		WorkflowID:      "unsealed-retired-resume-test",
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
	testutil.FailErr(t, "create state for paused unsealed retired run", err)

	// Resume must fail early without flipping run to Running
	_, err = h.WorkflowMgr.Resume(ctx, runID)
	if err == nil {
		t.Fatal("expected Resume to fail for unsealed retired workflow, got nil error")
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

func TestWorkflowRetiredRefusal_ResolveFeedbackAndDecisionRefuseUnsealed(t *testing.T) {
	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := people.WithCaller(context.Background(), people.Person{ID: "tester-1", Role: api.PersonRoleOwner})
	dir := h.ProjectDir(t, "security-survey-unsealed")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, dir)
	testutil.FailErr(t, "create session", err)

	entries := h.WorkflowMgr.Manifests.All()
	unsealedRetired := workflowdef.Manifest{
		ID:      "unsealed-retired-feedback-test",
		Version: "1.0.0",
		Name:    "Unsealed retired feedback test",
		Retired: true,
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "intake", Next: "review"},
			{ID: "review", Terminal: true},
		},
	}
	entries[workflowdef.ManifestKey(unsealedRetired.ID, unsealedRetired.Version)] = unsealedRetired
	h.WorkflowMgr.Manifests = workflowdef.NewRegistry(entries)

	now := time.Now().UTC()
	runID := "run-" + uuid.NewString()
	run := &api.WorkflowRun{
		ID:              runID,
		SessionID:       sess.ID,
		WorkflowID:      "unsealed-retired-feedback-test",
		WorkflowVersion: "1.0.0",
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "intake",
		CreatedAt:       now,
		UpdatedAt:       now,
		Revision:        1,
	}
	err = h.WorkflowMgr.Store.CreateState(ctx, run, dir, map[string]any{})
	testutil.FailErr(t, "create state for unsealed retired run", err)

	// ResolveUserFeedback must reject unsealed retired workflow
	_, err = h.WorkflowMgr.ResolveUserFeedback(ctx, sess.ID, runID, "intake", "user reply")
	if err == nil {
		t.Fatal("expected ResolveUserFeedback to fail for unsealed retired workflow, got nil error")
	}
	var unavailErr *workflow.WorkflowVersionUnavailableError
	if !errors.As(err, &unavailErr) {
		t.Fatalf("expected WorkflowVersionUnavailableError for ResolveUserFeedback, got %T: %v", err, err)
	}

	// ResolveUserDecision must reject unsealed retired workflow
	_, err = h.WorkflowMgr.ResolveUserDecision(ctx, sess.ID, runID, "intake", []string{"choice1"}, "")
	if err == nil {
		t.Fatal("expected ResolveUserDecision to fail for unsealed retired workflow, got nil error")
	}
	if !errors.As(err, &unavailErr) {
		t.Fatalf("expected WorkflowVersionUnavailableError for ResolveUserDecision, got %T: %v", err, err)
	}
}

func TestWorkflowRetiredRefusal_ResolveFeedbackRefusesMissingWorkflow(t *testing.T) {
	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := people.WithCaller(context.Background(), people.Person{ID: "tester-1", Role: api.PersonRoleOwner})
	dir := h.ProjectDir(t, "security-survey-missing")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, dir)
	testutil.FailErr(t, "create session", err)

	now := time.Now().UTC()
	missingRunID := "run-" + uuid.NewString()
	missingRun := &api.WorkflowRun{
		ID:              missingRunID,
		SessionID:       sess.ID,
		WorkflowID:      "nonexistent-workflow",
		WorkflowVersion: "9.9.9",
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "intake",
		CreatedAt:       now,
		UpdatedAt:       now,
		Revision:        1,
	}
	err = h.WorkflowMgr.Store.CreateState(ctx, missingRun, dir, map[string]any{})
	testutil.FailErr(t, "create state for missing workflow run", err)

	_, err = h.WorkflowMgr.ResolveUserFeedback(ctx, sess.ID, missingRunID, "intake", "user reply")
	if err == nil {
		t.Fatal("expected ResolveUserFeedback to fail for missing workflow, got nil error")
	}
	var unavailErr *workflow.WorkflowVersionUnavailableError
	if !errors.As(err, &unavailErr) {
		t.Fatalf("expected WorkflowVersionUnavailableError for missing workflow feedback, got %T: %v", err, err)
	}
}

func TestWorkflowRetiredRefusal_AutoAdvanceRefusesUnsealedRetiredVersion(t *testing.T) {
	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := context.Background()
	dir := h.ProjectDir(t, "security-survey-autoadvance")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, dir)
	testutil.FailErr(t, "create session", err)

	entries := h.WorkflowMgr.Manifests.All()
	unsealedRetired := workflowdef.Manifest{
		ID:      "unsealed-retired-autoadvance-test",
		Version: "1.0.0",
		Name:    "Unsealed retired autoadvance test",
		Retired: true,
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "intake", Next: "review"},
			{ID: "review", Terminal: true},
		},
	}
	entries[workflowdef.ManifestKey(unsealedRetired.ID, unsealedRetired.Version)] = unsealedRetired
	h.WorkflowMgr.Manifests = workflowdef.NewRegistry(entries)

	now := time.Now().UTC()
	runID := "run-" + uuid.NewString()
	run := &api.WorkflowRun{
		ID:              runID,
		SessionID:       sess.ID,
		WorkflowID:      "unsealed-retired-autoadvance-test",
		WorkflowVersion: "1.0.0",
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "intake",
		CreatedAt:       now,
		UpdatedAt:       now,
		Revision:        1,
	}
	err = h.WorkflowMgr.Store.CreateState(ctx, run, dir, map[string]any{})
	testutil.FailErr(t, "create state for unsealed retired run", err)

	_, err = h.WorkflowMgr.TryAutoAdvance(ctx, runID)
	if err == nil {
		t.Fatal("expected TryAutoAdvance to fail for unsealed retired workflow, got nil error")
	}
	var unavailErr *workflow.WorkflowVersionUnavailableError
	if !errors.As(err, &unavailErr) {
		t.Fatalf("expected WorkflowVersionUnavailableError for TryAutoAdvance, got %T: %v", err, err)
	}
	if unavailErr.RejectionCode() != "WORKFLOW_VERSION_UNAVAILABLE" {
		t.Fatalf("expected code WORKFLOW_VERSION_UNAVAILABLE, got %q", unavailErr.RejectionCode())
	}
}

func TestWorkflowRetiredRefusal_AssertRunnableRefusesUnsealedRetiredVersion(t *testing.T) {
	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := context.Background()
	dir := h.ProjectDir(t, "security-survey-assert-runnable")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, dir)
	testutil.FailErr(t, "create session", err)

	entries := h.WorkflowMgr.Manifests.All()
	unsealedRetired := workflowdef.Manifest{
		ID:      "unsealed-retired-assert-runnable-test",
		Version: "1.0.0",
		Name:    "Unsealed retired assert runnable test",
		Retired: true,
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "intake", Next: "review"},
			{ID: "review", Terminal: true},
		},
	}
	entries[workflowdef.ManifestKey(unsealedRetired.ID, unsealedRetired.Version)] = unsealedRetired
	h.WorkflowMgr.Manifests = workflowdef.NewRegistry(entries)

	now := time.Now().UTC()
	runID := "run-" + uuid.NewString()
	run := &api.WorkflowRun{
		ID:              runID,
		SessionID:       sess.ID,
		WorkflowID:      "unsealed-retired-assert-runnable-test",
		WorkflowVersion: "1.0.0",
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "intake",
		CreatedAt:       now,
		UpdatedAt:       now,
		Revision:        1,
	}
	err = h.WorkflowMgr.Store.CreateState(ctx, run, dir, map[string]any{})
	testutil.FailErr(t, "create state for unsealed retired run", err)

	err = h.WorkflowMgr.AssertRunnable(ctx, runID)
	if err == nil {
		t.Fatal("expected AssertRunnable to fail for unsealed retired workflow, got nil error")
	}
	var unavailErr *workflow.WorkflowVersionUnavailableError
	if !errors.As(err, &unavailErr) {
		t.Fatalf("expected WorkflowVersionUnavailableError for AssertRunnable, got %T: %v", err, err)
	}

	err = h.WorkflowMgr.AssertSessionRunnable(ctx, sess.ID)
	if err == nil {
		t.Fatal("expected AssertSessionRunnable to fail for unsealed retired workflow, got nil error")
	}
	if !errors.As(err, &unavailErr) {
		t.Fatalf("expected WorkflowVersionUnavailableError for AssertSessionRunnable, got %T: %v", err, err)
	}
}


