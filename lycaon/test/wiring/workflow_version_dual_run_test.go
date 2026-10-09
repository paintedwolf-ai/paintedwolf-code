package wiring

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkflowVersionDualRun_Concurrent100And200Isolation(t *testing.T) {
	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := context.Background()
	dirA := h.ProjectDir(t, "security-survey-a")
	dirB := h.ProjectDir(t, "security-survey-b")

	// Session A: runs 1.0.0
	sessA, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, dirA)
	testutil.FailErr(t, "create session A", err)

	now := time.Now().UTC()
	runIDA := "run-100-" + uuid.NewString()
	run100 := &api.WorkflowRun{
		ID:              runIDA,
		SessionID:       sessA.ID,
		WorkflowID:      "security-survey",
		WorkflowVersion: "1.0.0",
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "challenge",
		CreatedAt:       now,
		UpdatedAt:       now,
		Revision:        1,
	}
	err = h.WorkflowMgr.Store.State.CreateState(ctx, run100, dirA, map[string]any{})
	testutil.FailErr(t, "create state for 1.0.0 run", err)

	// Session B: runs 2.0.0
	sessB, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, dirB)
	testutil.FailErr(t, "create session B", err)

	runIDB := "run-200-" + uuid.NewString()
	run200 := &api.WorkflowRun{
		ID:              runIDB,
		SessionID:       sessB.ID,
		WorkflowID:      "security-survey",
		WorkflowVersion: "2.0.0",
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "challenge",
		CreatedAt:       now,
		UpdatedAt:       now,
		Revision:        1,
	}
	err = h.WorkflowMgr.Store.State.CreateState(ctx, run200, dirB, map[string]any{})
	testutil.FailErr(t, "create state for 2.0.0 run", err)

	// Verify both runs query anchors without interference
	match100 := anchor.RunMatch(runSource(run100), "phase", "challenge")
	binding100, err := anchor.RegistryFor(ctx, sessA.ID).ResolveInform(anchor.PhaseEntered, match100)
	testutil.FailErr(t, "ResolveInform for 1.0.0", err)
	if binding100 == nil || binding100.Render != "coordinator-security-challenge" {
		t.Fatalf("expected coordinator-security-challenge for 1.0.0, got %+v", binding100)
	}

	match200 := anchor.RunMatch(runSource(run200), "phase", "challenge")
	binding200, err := anchor.RegistryFor(ctx, sessB.ID).ResolveInform(anchor.PhaseEntered, match200)
	testutil.FailErr(t, "ResolveInform for 2.0.0", err)
	if binding200 == nil || binding200.Render != "coordinator-security-challenge" {
		t.Fatalf("expected coordinator-security-challenge for 2.0.0, got %+v", binding200)
	}

	// Each run reads gate feedback from its own version.
	active100, ok := h.WorkflowMgr.Policy.ActiveManifest(ctx, sessA.ID)
	if !ok || active100.Archive != "security-survey/1.0.0" {
		t.Fatalf("1.0.0 run archive = %+v, want security-survey/1.0.0", active100)
	}
	if active200, ok := h.WorkflowMgr.Policy.ActiveManifest(ctx, sessB.ID); !ok || active200.Archive != "" {
		t.Fatalf("2.0.0 run archive = %+v, want the live definition", active200)
	}

	catalog, err := feedback.LoadGateFeedbackCatalog()
	testutil.FailErr(t, "LoadGateFeedbackCatalog", err)
	cat100 := catalog.WithWorkflowArchive(active100.Archive)

	obs100 := cat100.ProjectObligations(ctx, []string{"evidence_passed:survey_challenged"}, "", nil)
	if len(obs100) == 0 || obs100[0].ID != "evidence_passed:survey_challenged" {
		t.Fatalf("expected obligation ID evidence_passed:survey_challenged, got %+v", obs100)
	}
	for _, step := range obs100[0].Satisfy {
		if strings.Contains(step, "NEEDS_INVESTIGATION") {
			t.Fatalf("1.0.0 gate feedback should not mention NEEDS_INVESTIGATION, got: %s", step)
		}
	}

	obs200 := catalog.ProjectObligations(ctx, []string{"evidence_passed:survey_challenged"}, "", nil)
	if len(obs200) == 0 || obs200[0].ID != "evidence_passed:survey_challenged" {
		t.Fatalf("expected obligation ID evidence_passed:survey_challenged, got %+v", obs200)
	}
	foundInvestigate := false
	for _, step := range obs200[0].Satisfy {
		if strings.Contains(step, "NEEDS_INVESTIGATION") {
			foundInvestigate = true
			break
		}
	}
	if !foundInvestigate {
		t.Fatalf("2.0.0 gate feedback should mention NEEDS_INVESTIGATION, got %+v", obs200[0].Satisfy)
	}
}
