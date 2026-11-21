package oar

import (
	"sync"
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
	prev := ActiveRuleBranchRecorder.Swap(rec)
	t.Cleanup(func() { ActiveRuleBranchRecorder.Store(prev) })

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

func TestRuleEvaluationWithDisabledAndConcurrentBranchRecorder(t *testing.T) {
	testutil.FailErr(t, "install anchor catalog", anchorcatalog.InstallBundled())
	testutil.FailErr(t, "install engine declaration", InstallEngineDeclarationBundled(""))

	ruleA := &Rule{
		ID:     "CONC_RULE_A",
		Kind:   KindPolicy,
		Anchor: AnchorToolPreInvoke,
		When:   `tool == "command"`,
		Effect: EffectBlock,
		NextSteps: &NextSteps{
			Steps: []NextStep{{
				Tool:      "recall",
				Args:      map[string]any{"query": "a"},
				Say:       "Use {{ call }}.",
				Otherwise: "Fallback A.",
			}},
		},
	}
	ruleB := &Rule{
		ID:     "CONC_RULE_B",
		Kind:   KindPolicy,
		Anchor: AnchorToolPreInvoke,
		When:   `tool == "edit"`,
		Effect: EffectBlock,
	}
	pipeline := NewGuardPipeline(NewRuleSet([]*Rule{ruleA, ruleB}), nil, NewCounterStore())
	pipeline.EnableAnchor(AnchorToolPreInvoke)

	// Disabled path: ActiveRuleBranchRecorder is nil.
	prev := ActiveRuleBranchRecorder.Swap(nil)
	t.Cleanup(func() { ActiveRuleBranchRecorder.Store(prev) })

	gcDisabled := NewGuardContext()
	gcDisabled.Invocation.Tool = "command"
	gcDisabled.Reach = toolreach.Offered([]string{"recall"})
	blocked, err := pipeline.EvaluateBlock(t.Context(), AnchorToolPreInvoke, gcDisabled)
	testutil.FailErr(t, "EvaluateBlock disabled", err)
	if blocked == nil || blocked.Decision == nil || blocked.Decision.Rule != "CONC_RULE_A" {
		t.Fatalf("blocked = %+v, want CONC_RULE_A", blocked)
	}
	if steps := ResolveNextSteps(ruleA, gcDisabled); len(steps.Calls) != 1 {
		t.Fatalf("len(steps.Calls) = %d, want 1", len(steps.Calls))
	}

	// Enabled concurrent path across distinct rules.
	rec := &RuleBranchRecorder{}
	ActiveRuleBranchRecorder.Store(rec)

	const n = 12
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			gc := NewGuardContext()
			switch idx % 3 {
			case 0:
				gc.Invocation.Tool = "command"
				gc.Reach = toolreach.Offered([]string{"recall"})
			case 1:
				gc.Invocation.Tool = "edit"
			default:
				gc.Invocation.Tool = "recall"
			}
			if _, evalErr := pipeline.EvaluateBlock(t.Context(), AnchorToolPreInvoke, gc); evalErr != nil {
				errCh <- evalErr
				return
			}
			_ = ResolveNextSteps(ruleA, gc)
		}(i)
	}
	wg.Wait()
	close(errCh)
	for evalErr := range errCh {
		testutil.FailErr(t, "concurrent EvaluateBlock", evalErr)
	}

	rec.Mu.Lock()
	hitA := rec.Hits["CONC_RULE_A#when"]
	hitAReach := rec.Hits["CONC_RULE_A#steps[0].reach"]
	hitB := rec.Hits["CONC_RULE_B#when"]
	rec.Mu.Unlock()

	if hitA == nil || hitA.TakenTrue != 4 || hitA.TakenFalse != 8 {
		t.Fatalf("CONC_RULE_A#when = %+v, want true=4 false=8", hitA)
	}
	if hitAReach == nil || hitAReach.TakenTrue != 4 || hitAReach.TakenFalse != 8 {
		t.Fatalf("CONC_RULE_A#steps[0].reach = %+v, want true=4 false=8", hitAReach)
	}
	if hitB == nil || hitB.TakenTrue != 4 || hitB.TakenFalse != 4 {
		t.Fatalf("CONC_RULE_B#when = %+v, want true=4 false=4", hitB)
	}
}
