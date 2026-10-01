package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPlanStubGateFailureSurfacesStructuredFeedback(t *testing.T) {
	t.Parallel()
	hintCfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	gateCfg, err := feedback.LoadGateFeedbackCatalog()
	contractcheck.FailErr(t, "LoadGateFeedbackCatalog", err)
	enricher := guidance.NewToolOutputEnricher(hintCfg, gateCfg)

	out := enricher.Enrich(t.Context(), guidance.EnrichInput{
		SessionID: "s-plan",
		Session:   &api.Session{ID: "s-plan", Posture: api.SessionPostureSpec},
		Tool:      "workflow_advance",
		Output:    `{"ok":false}`,
		Workflow: feedback.WorkflowEvaluationContext{
			WorkflowID: "plan", CurrentPhase: "expand",
			FailedLeaves: []string{"plan_stub_valid"}, RunActive: true,
		},
	}).Output
	assertGateFeedbackShape(t, out, "plan_stub_valid")
}

func TestChildRunCompleteFailureFeedback(t *testing.T) {
	t.Parallel()
	hintCfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	gateCfg, err := feedback.LoadGateFeedbackCatalog()
	contractcheck.FailErr(t, "LoadGateFeedbackCatalog", err)
	enricher := guidance.NewToolOutputEnricher(hintCfg, gateCfg)

	out := enricher.Enrich(t.Context(), guidance.EnrichInput{
		SessionID: "s-plan",
		Session:   &api.Session{ID: "s-plan", Posture: api.SessionPostureSpec},
		Tool:      "workflow_advance",
		Output:    `{"ok":false}`,
		Workflow: feedback.WorkflowEvaluationContext{
			WorkflowID: "plan", CurrentPhase: "execute",
			FailedLeaves: []string{"child_run_complete"}, RunActive: true,
		},
	}).Output
	assertGateFeedbackShape(t, out, "child_run_complete")
}

func assertGateFeedbackShape(t *testing.T, out, gateID string) {
	t.Helper()
	if !strings.Contains(out, "Gate blocked: "+gateID) {
		t.Fatalf("missing gate header for %q:\n%s", gateID, out)
	}
	if !strings.Contains(out, "What's missing:") {
		t.Fatalf("missing matched signals section for %q:\n%s", gateID, out)
	}
	if !strings.Contains(out, "To satisfy:") {
		t.Fatalf("missing satisfy steps for %q:\n%s", gateID, out)
	}
	if !strings.Contains(out, "Code: WORKFLOW_GATE_BLOCKED") {
		t.Fatalf("missing WORKFLOW_GATE_BLOCKED code for %q:\n%s", gateID, out)
	}
}
