package tools

import (
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolreach"
)

func hintRules(id string, steps ...oar.NextStep) *oar.RuleSet {
	return oar.NewRuleSet([]*oar.Rule{{
		ID:        id,
		NextSteps: &oar.NextSteps{Steps: steps},
	}})
}

func TestHintNextStepsResolvesOnlyReachableSteps(t *testing.T) {
	rules := hintRules("HINT", oar.NextStep{Tool: "recall", Say: "Recover with {{ call }}.", Otherwise: "Re-observe it."})
	reachCtx := WithCallReach(t.Context(), toolreach.Offered([]string{"recall"}))
	if got := HintNextSteps(reachCtx, rules, "HINT", nil); got != "Recover with `recall(…)`." {
		t.Fatalf("reachable text = %q", got)
	}
	if got := HintNextSteps(t.Context(), rules, "HINT", nil); got != "Re-observe it." {
		t.Fatalf("unreachable text = %q", got)
	}
}

func TestHintNextStepsBindsInvocationTool(t *testing.T) {
	rules := hintRules("REISSUE", oar.NextStep{Say: "Reissue with {{ call }}."})
	ctx := WithCallReach(t.Context(), toolreach.Offered([]string{"read"}))
	if got := HintNextSteps(ctx, rules, "REISSUE", map[string]any{"tool": "read"}); got != "Reissue with `read(…)`." {
		t.Fatalf("reissue text = %q", got)
	}
}

func TestHintNextStepsEmptyWithoutRuleOrCatalog(t *testing.T) {
	ctx := t.Context()
	if got := HintNextSteps(ctx, nil, "HINT", nil); got != "" {
		t.Fatalf("nil rules text = %q", got)
	}
	rules := oar.NewRuleSet([]*oar.Rule{{ID: "BARE"}})
	if got := HintNextSteps(ctx, rules, "HINT", nil); got != "" {
		t.Fatalf("unknown code text = %q", got)
	}
	if got := HintNextSteps(ctx, rules, "BARE", nil); got != "" {
		t.Fatalf("rule without catalog text = %q", got)
	}
}

func TestHintNextStepsResolverDelegates(t *testing.T) {
	rules := hintRules("HINT", oar.NextStep{Tool: "recall", Say: "Recover with {{ call }}."})
	ctx := WithCallReach(t.Context(), toolreach.Offered([]string{"recall"}))
	if got := HintNextStepsResolver(rules)(ctx, "HINT", nil); got != "Recover with `recall(…)`." {
		t.Fatalf("resolver text = %q", got)
	}
	if got := HintNextStepsResolver(nil)(ctx, "HINT", nil); got != "" {
		t.Fatalf("nil-rules resolver text = %q", got)
	}
}
