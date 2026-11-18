package contract

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil/exposure"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestFeedbackHintRequiresPendingFeedbackInContextBlock(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	ctx := api.CoordinatorRunContext{
		PendingFeedback: &api.PendingFeedback{PhaseID: "clarify", Prompt: "REST or GraphQL?"},
	}
	codes := surface.StaticWorkflowHintCodes(ctx, false)
	found := false
	for _, code := range codes {
		if code == "WORKFLOW_FEEDBACK_PENDING" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected WORKFLOW_FEEDBACK_PENDING hint")
	}
	block, err := renderCoordinatorInjectForTest(t, ctx, codes, cfg)
	contractcheck.FailErr(t, "renderCoordinatorInjectForTest failed", err)
	if !strings.Contains(block, "pending_feedback") {
		t.Fatal("context block missing pending_feedback when hint emitted")
	}
	if !strings.Contains(block, "clarify") {
		t.Fatal("context block missing feedback phase id")
	}
}

func TestGateUnmetHintRequiresFailedLeavesInContextBlock(t *testing.T) {
	t.Parallel()
	const leaf = "human_approval_fixture_leaf"
	ctx := api.CoordinatorRunContext{FailedLeaves: []string{leaf}}
	codes := surface.StaticWorkflowHintCodes(ctx, false)
	if !slices.Contains(codes, "WORKFLOW_GATE_UNMET") {
		t.Fatalf("hint codes = %v, want WORKFLOW_GATE_UNMET for a failed leaf", codes)
	}
	if slices.Contains(surface.StaticWorkflowHintCodes(api.CoordinatorRunContext{}, false), "WORKFLOW_GATE_UNMET") {
		t.Fatal("WORKFLOW_GATE_UNMET without a failed leaf")
	}
	block, err := renderCoordinatorInjectForTest(t, ctx, codes, nil)
	contractcheck.FailErr(t, "renderCoordinatorInjectForTest failed", err)
	exposure.Present(t, block, leaf)
}

func renderCoordinatorInjectForTest(t *testing.T, ctx api.CoordinatorRunContext, codes []string, hints *guidance.HintConfig) (string, error) {
	t.Helper()
	renderer := prompts.NewInjectRenderer(contractcheck.BundledPromptEngineForRoot(t))
	return inject.RenderActiveWorkflowInject(context.Background(), renderer, "sess-inject-test", inject.CoordinatorTurnFrame{RunContext: ctx}, hints, codes, nil)
}
