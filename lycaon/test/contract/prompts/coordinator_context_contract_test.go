package contract

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
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

func TestLastUserAskResponseAbsentFromInjectBlock(t *testing.T) {
	t.Parallel()
	ctx := api.CoordinatorRunContext{
		PendingFeedback: &api.PendingFeedback{PhaseID: "ask-test-1", Prompt: "use REST?"},
	}
	block, err := renderCoordinatorInjectForTest(t, ctx, nil, nil)
	contractcheck.FailErr(t, "renderCoordinatorInjectForTest failed", err)
	if strings.Contains(block, "last_user_ask_response") {
		t.Fatal("deleted last_user_ask_response from inject")
	}
	if !strings.Contains(block, "pending_feedback") {
		t.Fatal("context block missing pending_feedback")
	}
}

func TestGateUnmetHintRequiresFailedLeavesInContextBlock(t *testing.T) {
	t.Parallel()
	ctx := api.CoordinatorRunContext{FailedLeaves: []string{"human_approval"}}
	codes := surface.StaticWorkflowHintCodes(ctx, false)
	block, err := renderCoordinatorInjectForTest(t, ctx, codes, nil)
	contractcheck.FailErr(t, "renderCoordinatorInjectForTest failed", err)
	if !strings.Contains(block, "failed_leaves") {
		t.Fatal("context block missing failed_leaves when WORKFLOW_GATE_UNMET would fire")
	}
}

func TestComposeSummaryFieldsMappedToCoordinatorRunContext(t *testing.T) {
	t.Parallel()
	summaryType := reflect.TypeOf(api.ComposeEffectiveSummary{})
	contextType := reflect.TypeOf(api.CoordinatorRunContext{})
	pairs := []struct {
		summaryField string
		contextField string
	}{
		{"CoordinatorBrief", "CoordinatorBrief"},
		{"RequiresIsolation", "RequiresIsolation"},
		{"Feedback", "FeedbackPhases"},
		{"Decisions", "DecisionPhases"},
	}
	for _, pair := range pairs {
		sf, ok := summaryType.FieldByName(pair.summaryField)
		if !ok {
			t.Fatalf("ComposeEffectiveSummary missing field %q", pair.summaryField)
		}
		cf, ok := contextType.FieldByName(pair.contextField)
		if !ok {
			t.Fatalf("CoordinatorRunContext missing field %q", pair.contextField)
		}
		sJSON := sf.Tag.Get("json")
		cJSON := cf.Tag.Get("json")
		sName := strings.TrimSuffix(strings.Split(sJSON, ",")[0], "omitempty")
		cName := strings.TrimSuffix(strings.Split(cJSON, ",")[0], "omitempty")
		if sName != cName {
			t.Fatalf("json tag mismatch %q (%s) vs %q (%s)", pair.summaryField, sName, pair.contextField, cName)
		}
	}
}

func renderCoordinatorInjectForTest(t *testing.T, ctx api.CoordinatorRunContext, codes []string, hints *guidance.HintConfig) (string, error) {
	t.Helper()
	renderer := prompts.NewInjectRenderer(contractcheck.BundledPromptEngineForRoot(t))
	return inject.RenderActiveWorkflowInject(context.Background(), renderer, "sess-inject-test", inject.CoordinatorTurnFrame{RunContext: ctx}, hints, codes, nil)
}
