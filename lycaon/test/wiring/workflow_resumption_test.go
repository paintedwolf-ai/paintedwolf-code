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
	"github.com/lycaon/lycaon/internal/workflow"
	"github.com/lycaon/lycaon/pkg/api"
)

// runGateFeedback returns the gate feedback the session's active run reads.
func runGateFeedback(t *testing.T, h *Harness, sessionID string) *feedback.GateFeedbackCatalog {
	t.Helper()
	stock, err := feedback.LoadGateFeedbackCatalog()
	testutil.FailErr(t, "LoadGateFeedbackCatalog", err)
	active, ok := h.WorkflowMgr.ActiveManifest(context.Background(), sessionID)
	if !ok {
		t.Fatal("session has no active workflow manifest")
	}
	return stock.WithWorkflowArchive(active.Archive)
}

type testRunSource struct {
	workflowID      string
	workflowVersion string
}

func (s testRunSource) WorkflowIdentity() (string, string) {
	return s.workflowID, s.workflowVersion
}

func runSource(run *api.WorkflowRun) anchor.RunSource {
	if run == nil {
		return nil
	}
	return testRunSource{
		workflowID:      run.WorkflowID,
		workflowVersion: run.WorkflowVersion,
	}
}

func TestWorkflowResumption_100RetainsPromptRulesAndGateFeedback(t *testing.T) {
	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := context.Background()
	dir := h.ProjectDir(t, "security-survey")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, dir)
	testutil.FailErr(t, "create session", err)

	// Simulate an existing pre-v1.0.1 run on 1.0.0
	now := time.Now().UTC()
	runID := "run-100-" + uuid.NewString()
	run100 := &api.WorkflowRun{
		ID:              runID,
		SessionID:       sess.ID,
		WorkflowID:      "security-survey",
		WorkflowVersion: "1.0.0",
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "claims",
		CreatedAt:       now,
		UpdatedAt:       now,
		Revision:        1,
	}
	err = h.WorkflowMgr.Store.CreateState(ctx, run100, dir, map[string]any{})
	testutil.FailErr(t, "create state for 1.0.0 run", err)

	// Manifest resolution must resolve 1.0.0 manifest
	manifest, err := h.WorkflowMgr.ManifestForRunID(ctx, runID)
	testutil.FailErr(t, "ManifestForRunID", err)
	if manifest.Version != "1.0.0" {
		t.Fatalf("expected manifest version 1.0.0, got %q", manifest.Version)
	}

	// Satisfy evidence_passed:survey_claims gate so advance can proceed
	vars, err := h.WorkflowMgr.Store.GetScaffoldVars(ctx, runID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars = workflow.SetGateSatisfied(vars, "evidence_passed:survey_claims", true)
	err = h.WorkflowMgr.Store.UpdateVars(ctx, run100, dir, vars)
	testutil.FailErr(t, "UpdateVars", err)

	// Advance must succeed (1.0.0 is retired but resumable)
	runAfter, err := h.WorkflowMgr.Advance(ctx, runID)
	testutil.FailErr(t, "Advance 1.0.0 run to challenge", err)
	if runAfter.CurrentPhase != "challenge" {
		t.Fatalf("expected current phase challenge, got %q", runAfter.CurrentPhase)
	}

	// Prompt matching via anchor.RunMatch must match 1.0.0 challenge prompt
	matchCtx := anchor.RunMatch(runSource(runAfter), "phase", "challenge")
	binding, err := anchor.RegistryFor(ctx, sess.ID).ResolveInform(anchor.PhaseEntered, matchCtx)
	testutil.FailErr(t, "ResolveInform for 1.0.0 challenge phase", err)
	if binding == nil {
		t.Fatal("expected binding for 1.0.0 challenge phase, got nil")
	}
	if binding.Render != "coordinator-security-challenge" {
		t.Fatalf("expected render coordinator-security-challenge for 1.0.0, got %q", binding.Render)
	}

	// Gate feedback resolution for 1.0.0 resolves the archived copy of
	// evidence_passed:survey_challenged, which accepts only CHALLENGED and never
	// mentions NEEDS_INVESTIGATION (the live 2.0.0 feedback adds it).
	archiveCatalog := runGateFeedback(t, h, sess.ID)
	obs := archiveCatalog.ProjectObligations(ctx, []string{"evidence_passed:survey_challenged"}, "", map[string]any{})
	if len(obs) == 0 {
		t.Fatal("expected obligation for evidence_passed:survey_challenged")
	}
	for _, step := range obs[0].Satisfy {
		if strings.Contains(step, "NEEDS_INVESTIGATION") {
			t.Fatalf("sealed 1.0.0 gate feedback should not mention NEEDS_INVESTIGATION, got: %s", step)
		}
	}
}

func TestWorkflowResumption_100AdvancesThroughEveryPhase(t *testing.T) {
	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := context.Background()
	dir := h.ProjectDir(t, "security-survey")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, dir)
	testutil.FailErr(t, "create session", err)

	now := time.Now().UTC()
	runID := "run-100-all-phases-" + uuid.NewString()
	run100 := &api.WorkflowRun{
		ID:              runID,
		SessionID:       sess.ID,
		WorkflowID:      "security-survey",
		WorkflowVersion: "1.0.0",
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "plan",
		CreatedAt:       now,
		UpdatedAt:       now,
		Revision:        1,
	}
	err = h.WorkflowMgr.Store.CreateState(ctx, run100, dir, map[string]any{})
	testutil.FailErr(t, "create state for 1.0.0 run", err)

	type phaseStep struct {
		phase    string
		gate     string
		expected string
	}
	steps := []phaseStep{
		{phase: "plan", gate: "fanout_planned", expected: "execute"},
		{phase: "execute", gate: "worker_cycle_ready", expected: "claims"},
		{phase: "claims", gate: "evidence_passed:survey_claims", expected: "challenge"},
		{phase: "challenge", gate: "evidence_passed:survey_challenged", expected: "report"},
		{phase: "report", gate: "topology_report_delivered", expected: "done"},
	}

	for _, step := range steps {
		vars, err := h.WorkflowMgr.Store.GetScaffoldVars(ctx, runID)
		testutil.FailErr(t, "GetScaffoldVars in "+step.phase, err)
		vars = workflow.SetGateSatisfied(vars, step.gate, true)
		if step.phase == "execute" {
			vars["worker_cycle"] = map[string]any{"evaluating": true, "summary_status": "complete"}
		}
		run100, err = h.WorkflowMgr.Get(ctx, runID)
		testutil.FailErr(t, "Get run in "+step.phase, err)
		err = h.WorkflowMgr.Store.UpdateVars(ctx, run100, dir, vars)
		testutil.FailErr(t, "UpdateVars in "+step.phase, err)

		run100, err = h.WorkflowMgr.Advance(ctx, runID)
		testutil.FailErr(t, "Advance from "+step.phase, err)
		if run100.CurrentPhase != step.expected {
			t.Fatalf("after advancing from %s, expected phase %q, got %q", step.phase, step.expected, run100.CurrentPhase)
		}
	}

	if run100.Status != api.WorkflowRunStatusComplete {
		t.Fatalf("expected completed status at terminal phase, got %s", run100.Status)
	}
}
