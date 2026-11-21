package oar

import (
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolreach"
)

func TestRuleBranchRecorderSeparatesWitnessesFromMockScenarios(t *testing.T) {
	testutil.FailErr(t, "install anchor catalog", anchorcatalog.InstallBundled())
	testutil.FailErr(t, "install engine declaration", InstallEngineDeclarationBundled(""))

	rule := &Rule{
		ID:     "TEST_BRANCH_RULE",
		Kind:   KindPolicy,
		Anchor: AnchorToolPreInvoke,
		When:   `tool == "command"`,
		Effect: EffectBlock,
		NextSteps: &NextSteps{
			Steps: []NextStep{{
				When:      `tool == "command"`,
				Tool:      "recall",
				Args:      map[string]any{"query": "main"},
				Say:       "Search first with {{ call }}.",
				Otherwise: "Use another available tool.",
			}},
		},
	}
	pipeline := NewGuardPipeline(NewRuleSet([]*Rule{rule}), nil, NewCounterStore())
	pipeline.EnableAnchor(AnchorToolPreInvoke)

	rec := &RuleBranchRecorder{}
	ActiveRuleBranchRecorder.Store(rec)
	t.Cleanup(func() { ActiveRuleBranchRecorder.Store(nil) })

	// Production GuardContext witnesses: one true, one false on rule.When.
	gcTrue := NewGuardContext()
	gcTrue.Invocation.Tool = "command"
	gcTrue.Reach = toolreach.Offered([]string{"recall"})
	_, err := pipeline.EvaluateBlock(t.Context(), AnchorToolPreInvoke, gcTrue)
	testutil.FailErr(t, "EvaluateBlock true", err)
	_ = ResolveNextSteps(rule, gcTrue)

	gcFalse := NewGuardContext()
	gcFalse.Invocation.Tool = "recall"
	_, err = pipeline.EvaluateBlock(t.Context(), AnchorToolPreInvoke, gcFalse)
	testutil.FailErr(t, "EvaluateBlock false", err)

	gcUnreachable := NewGuardContext()
	gcUnreachable.Invocation.Tool = "command"
	gcUnreachable.Reach = toolreach.Offered(nil)
	_ = ResolveNextSteps(rule, gcUnreachable)

	// Mock scenario context must increment ScenarioTrue, not TakenTrue.
	scCtx := factsToContext(map[string]any{"tool": "command"})
	_, err = pipeline.EvaluateBlock(t.Context(), AnchorToolPreInvoke, scCtx)
	testutil.FailErr(t, "EvaluateBlock scenario", err)

	whenHit := rec.Hits["TEST_BRANCH_RULE#when"]
	if whenHit == nil || whenHit.TakenTrue != 1 || whenHit.TakenFalse != 1 || whenHit.ScenarioTrue != 1 {
		t.Fatalf("unexpected when hit: %+v", whenHit)
	}
	reachHit := rec.Hits["TEST_BRANCH_RULE#steps[0].reach"]
	if reachHit == nil || reachHit.TakenTrue != 1 || reachHit.TakenFalse != 1 {
		t.Fatalf("unexpected step reach hit: %+v", reachHit)
	}
}
