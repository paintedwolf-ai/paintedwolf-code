package contract

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const workflowGateBlockedCode = "WORKFLOW_GATE_BLOCKED"

// A blocked workflow_advance on any catalogued gate raises one
// WORKFLOW_GATE_BLOCKED rejection carrying that gate's own feedback.
func TestEveryGateFailureRejectsAdvanceWithItsFeedback(t *testing.T) {
	t.Parallel()
	enricher, catalog := gateFeedbackEnricher(t)
	if len(catalog.GateIDs()) == 0 {
		t.Fatal("no gate feedback keys loaded")
	}
	for _, gateID := range catalog.GateIDs() {
		t.Run(gateID, func(t *testing.T) {
			t.Parallel()
			wf := feedback.WorkflowEvaluationContext{
				WorkflowID: "plan", CurrentPhase: "expand",
				FailedLeaves: []string{gateID}, RunActive: true,
			}
			res := enrichGateFailure(t, enricher, workflowAdvanceTool, wf)
			assertGateBlocked(t, res, api.ToolResultOutcomeRejected)
			own, err := catalog.RenderGateFeedback(t.Context(), gateID, feedback.GateFeedbackContext(wf, nil))
			contractcheck.FailErr(t, "RenderGateFeedback", err)
			if own == "" || !strings.Contains(own, gateID) {
				t.Fatalf("gate %s feedback does not carry its id", gateID)
			}
			if !strings.Contains(res.Output, own) {
				t.Fatalf("enriched output does not carry gate %s feedback", gateID)
			}
		})
	}
}

// On a tool other than workflow_advance an actionable gate is a banner, not a
// rejection; human approval and evidence leaves are not the tool's to act on.
func TestActionableGateFailureBannersOtherTools(t *testing.T) {
	t.Parallel()
	enricher, catalog := gateFeedbackEnricher(t)
	for _, gateID := range catalog.GateIDs() {
		t.Run(gateID, func(t *testing.T) {
			t.Parallel()
			wf := feedback.WorkflowEvaluationContext{
				WorkflowID: "plan", CurrentPhase: "execute",
				FailedLeaves: []string{gateID}, RunActive: true,
			}
			res := enrichGateFailure(t, enricher, "write", wf)
			if len(feedback.ActionableFailedLeaves("write", []string{gateID})) == 0 {
				if res.Facts.HasCode(workflowGateBlockedCode) {
					t.Fatalf("non-actionable gate %s raised %s on write", gateID, workflowGateBlockedCode)
				}
				return
			}
			assertGateBlocked(t, res, "")
		})
	}
}

func gateFeedbackEnricher(t *testing.T) (*guidance.ToolOutputEnricher, *feedback.GateFeedbackCatalog) {
	t.Helper()
	hintCfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	gateCfg, err := feedback.LoadGateFeedbackCatalog()
	contractcheck.FailErr(t, "LoadGateFeedbackCatalog", err)
	return guidance.NewToolOutputEnricher(hintCfg, gateCfg, nil), gateCfg
}

func enrichGateFailure(t *testing.T, enricher *guidance.ToolOutputEnricher, tool string, wf feedback.WorkflowEvaluationContext) guidance.EnrichResult {
	t.Helper()
	return enricher.Enrich(t.Context(), guidance.EnrichInput{
		SessionID: "s-plan",
		Session:   &api.Session{ID: "s-plan", Posture: api.SessionPostureSpec},
		Tool:      tool,
		Output:    `{"ok":false}`,
		Workflow:  wf,
	})
}

func assertGateBlocked(t *testing.T, res guidance.EnrichResult, outcome api.ToolResultOutcome) {
	t.Helper()
	if n := len(slices.DeleteFunc(slices.Clone(res.Facts.Codes), func(c string) bool { return c != workflowGateBlockedCode })); n != 1 {
		t.Fatalf("codes = %v, want %s once", res.Facts.Codes, workflowGateBlockedCode)
	}
	if res.Facts.Outcome != outcome {
		t.Fatalf("outcome = %q want %q", res.Facts.Outcome, outcome)
	}
}
