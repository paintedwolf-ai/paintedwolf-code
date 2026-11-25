package oar

import (
	"errors"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolreach"
)

var errFactUnavailable = errors.New("fact unavailable")

func failingFactContext(t *testing.T) (*GuardContext, *int) {
	t.Helper()
	gc := reachContext(toolreach.Offered([]string{"recall", "process_list", "ask_user"}))
	listedCalls := 0
	gc.RegisterProvider("paintedwolf.session_role", func(*GuardContext) error { return errFactUnavailable })
	gc.RegisterProvider("paintedwolf.listed", func(*GuardContext) error {
		listedCalls++
		return nil
	})
	return gc, &listedCalls
}

func TestResolveNextStepsOmitsAStepWhoseFactsFail(t *testing.T) {
	rule := stepRule(
		NextStep{When: "paintedwolf.session_role == 'worker'", Tool: "recall", Say: "{% if load %}{{ load }}, then {% endif %}{{ call }}", Otherwise: "fallback",
			Or: []NextStep{{Tool: "ask_user", Say: "{% if load %}{{ load }}, then {% endif %}{{ call }}"}}},
		NextStep{When: "paintedwolf.listed", Tool: "process_list", Complete: true, Say: "{% if load %}{{ load }}, then {% endif %}{{ call }}", Otherwise: "{{ paintedwolf.listed }}"},
	)
	gc, listedCalls := failingFactContext(t)
	ActiveNextStepObservers.Store(t.Name(), NextStepObserver(func(*Rule, ResolvedSteps) {}))
	t.Cleanup(func() { ActiveNextStepObservers.Delete(t.Name()) })

	got := ResolveNextSteps(rule, gc)

	if !errors.Is(got.Err, errFactUnavailable) {
		t.Fatalf("err = %v, want the provider failure", got.Err)
	}
	if *listedCalls != 1 {
		t.Fatalf("later step's provider ran %d times, want 1", *listedCalls)
	}
	var tools []string
	for _, call := range got.Calls {
		tools = append(tools, call.Tool)
	}
	if len(tools) != 0 {
		t.Fatalf("calls = %v, want none: step 0 omitted and step 1 guard false", tools)
	}
	outcomes := map[string]StepResolutionOutcome{}
	for _, resolution := range got.Resolutions {
		outcomes[resolution.BranchPrefix] = resolution.Outcome
	}
	if outcomes["steps[0]"] != StepOutcomeFactsUnavailable || outcomes["steps[0].or[0]"] != StepOutcomeUnselected {
		t.Fatalf("outcomes = %v, want step 0 facts unavailable and its alternative unselected", outcomes)
	}
}

func TestResolveNextStepsReportsNoErrorWhenFactsAssemble(t *testing.T) {
	rule := stepRule(NextStep{When: "paintedwolf.listed", Tool: "process_list", Complete: true, Say: "{% if load %}{{ load }}, then {% endif %}{{ call }}"})
	gc, _ := failingFactContext(t)
	gc.RegisterProvider("paintedwolf.listed", func(g *GuardContext) error { return nil })
	got := ResolveNextSteps(rule, gc)
	testutil.FailErr(t, "resolve", got.Err)
}

func TestFiredRuleTraceRecordsNextStepFactFailure(t *testing.T) {
	rule := &Rule{
		ID: "STEP_FACTS", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectWarn, Enforcement: "enforce",
		Copy: Copy{Title: "t", Fix: "{{ " + NextStepsFact + " }}"},
		NextSteps: &NextSteps{Steps: []NextStep{
			{When: "paintedwolf.session_role == 'worker'", Tool: "recall", Say: "{% if load %}{{ load }}, then {% endif %}{{ call }}"},
		}},
	}
	pipeline := NewGuardPipeline(NewRuleSet([]*Rule{rule}), nil, nil)
	pipeline.EnableAnchor(AnchorToolPreInvoke)
	gc, _ := failingFactContext(t)
	res, err := pipeline.EvaluateBlock(t.Context(), AnchorToolPreInvoke, gc)
	testutil.FailErr(t, "evaluate", err)
	if res.Decision == nil || !errors.Is(res.Decision.NextStepsErr, errFactUnavailable) {
		t.Fatalf("decision = %#v, want next-steps fact failure", res.Decision)
	}
	if !slices.ContainsFunc(res.Trace.Entries, func(e TraceEntry) bool { return e.Rule == "STEP_FACTS" && e.Error != "" }) {
		t.Fatalf("trace = %#v, want the fired entry to carry the failure", res.Trace.Entries)
	}
}
