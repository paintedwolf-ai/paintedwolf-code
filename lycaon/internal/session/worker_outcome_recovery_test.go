package session_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFinalizeWorkerSummaryParksOnAPendingDecision(t *testing.T) {
	resolver := &stubSummaryResolver{msgs: scoutSurveyFixtureMessages()}
	opts := finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{
		DecisionPending: func(context.Context, string) bool { return true },
	})
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(
		context.Background(), resolver, resolver, "child", "skeptic", opts,
	)
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != string(api.WorkerSummaryStatusNeedsDecision) {
		t.Fatalf("status = %q want needs_decision", out.Status)
	}
	if resolver.prompts != 0 {
		t.Fatalf("prompts = %d want 0 — a parked worker is not given a forced closeout turn", resolver.prompts)
	}
}

func TestFinalizeWorkerSummaryStillClosesOutWithoutAPendingDecision(t *testing.T) {
	resolver := &stubSummaryResolver{msgs: scoutSurveyFixtureMessages()}
	opts := finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{
		DecisionPending: func(context.Context, string) bool { return false },
	})
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(
		context.Background(), resolver, resolver, "child", "skeptic", opts,
	)
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status == string(api.WorkerSummaryStatusNeedsDecision) {
		t.Fatalf("status = %q — nothing was pending, so the closeout path still handles this", out.Status)
	}
}

func TestFinalizeWorkerSummaryBudgetCountsUnicodeCharacters(t *testing.T) {
	brief := strings.Repeat("界🚀", 25)
	resolver := &stubSummaryResolver{msgs: append(scoutSurveyFixtureMessages(), completeLegToolRow(map[string]any{
		"leg_status": "complete", "brief": brief,
	}))}
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(t.Context(), resolver, resolver, "child", "path-explorer", finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{
		MaxChars: 50, Pipeline: sessionTestOARPipeline(t),
	}))
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if resolver.prompts != 0 || out.Summary != brief || out.Status != "complete" {
		t.Fatalf("in-budget Unicode report was trimmed or lost: prompts=%d outcome=%+v", resolver.prompts, out)
	}
}

func completeLegNamedRow(id string, args map[string]any) api.Message {
	message := completeLegToolRow(args)
	message.ID = id
	return message
}
func completeLegJSONRow(t *testing.T, id, raw string) api.Message {
	t.Helper()
	var args map[string]any
	testutil.FailErr(t, "decode complete_leg fixture", json.Unmarshal([]byte(raw), &args))
	return completeLegNamedRow(id, args)
}
