package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
)

// HintNextSteps resolves a hint code's checked next steps against the frozen
// request carried on ctx. The hint's occurrence data supplies the facts the
// steps bind; a rule without a catalog, or a step the request cannot reach,
// resolves empty ([OAR-FACT-25]).
func HintNextSteps(ctx context.Context, rules *oar.RuleSet, code string, data map[string]any) string {
	if rules == nil {
		return ""
	}
	rule, ok := rules.Get(code)
	if !ok || rule.NextSteps == nil {
		return ""
	}
	gc := oar.NewGuardContext()
	gc.Reach = CallReach(ctx)
	if tool, _ := data["tool"].(string); tool != "" {
		gc.Invocation.Tool = tool
	}
	gc.ObservationData = data
	return oar.ResolveNextSteps(rule, gc).Text
}

// HintNextStepsResolver binds a RuleSet into the guidance resolver signature.
func HintNextStepsResolver(rules *oar.RuleSet) guidance.NextStepsResolver {
	return func(ctx context.Context, code string, data map[string]any) string {
		return HintNextSteps(ctx, rules, code, data)
	}
}
