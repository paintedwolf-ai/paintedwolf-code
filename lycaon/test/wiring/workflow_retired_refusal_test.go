package wiring

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
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
