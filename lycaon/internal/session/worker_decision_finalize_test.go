package session_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWorkerFinalizationSuspendsDespiteBlockedCompletion(t *testing.T) {
	resolver := &stubSummaryResolver{msgs: append(scoutSurveyFixtureMessages(), completeLegToolRow(map[string]any{
		"leg_status": "blocked", "brief": "Parser blocked writes; decision requested.",
	}))}
	decisionChecks := 0
	opts := finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{
		DecisionPending: func(context.Context, string) bool { decisionChecks++; return true },
	})
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(t.Context(), resolver, "child", "implementer", opts)
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	t.Logf("status=%s provenance=%s pending-decision checks=%d", out.Status, out.Provenance, decisionChecks)
	if out.Status != "needs_decision" || decisionChecks == 0 {
		t.Fatalf("unexpected result: %+v checks=%d", out, decisionChecks)
	}
}

func TestDecisionRequestedDuringCloseoutStopsFinalizationPrompts(t *testing.T) {
	resolver := &stubSummaryResolver{msgs: scoutSurveyFixtureMessages()}
	opts := finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{
		DecisionPending: func(context.Context, string) bool { return resolver.prompts > 0 },
	})
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(t.Context(), resolver, "child", "implementer", opts)
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != "needs_decision" || resolver.prompts != 1 {
		t.Fatalf("status=%s prompts=%d", out.Status, resolver.prompts)
	}
}
