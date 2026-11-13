package oar

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestConcurrentEvaluationsKeepRulesSessionAndSnapshotTogether(t *testing.T) {
	readA := &Rule{ID: "READ", Namespace: "publisher.a", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectBlock, Enforcement: "enforce", When: `paintedwolf.workers_idle && paintedwolf.fire_count == 1`}
	readB := &Rule{ID: "READ", Namespace: "publisher.b", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectBlock, Enforcement: "enforce", When: `paintedwolf.workers_idle && paintedwolf.fire_count == 1`}
	rulesA := NewRuleSet([]*Rule{readA})
	rulesB := NewRuleSet([]*Rule{readB})
	loader, err := NewLoader("")
	testutil.FailErr(t, "create loader", err)
	pipeline := NewGuardPipeline(rulesA, loader, NewCounterStore())
	pipeline.EnableAnchor(AnchorToolPreInvoke)
	pipeline.SetRuleSetFor(func(_ context.Context, sessionID string) *RuleSet {
		if sessionID == "session-b" {
			return rulesB
		}
		return rulesA
	})
	pipeline.Counters().Increment("session-a", "publisher.a/READ", CounterFire, 1)

	var ready sync.WaitGroup
	ready.Add(2)
	start := make(chan struct{})
	type evaluation struct {
		result *PipelineResult
		err    error
	}
	evaluate := func(sessionID string) evaluation {
		gc := NewGuardContext()
		gc.Session.SessionID = sessionID
		gc.RegisterProvider("paintedwolf.workers_idle", func(target *GuardContext) error {
			ready.Done()
			<-start
			target.Workers.WorkersIdle = true
			return nil
		})
		res, evalErr := pipeline.EvaluateBlock(t.Context(), AnchorToolPreInvoke, gc)
		return evaluation{result: res, err: evalErr}
	}

	results := make(chan evaluation, 2)
	go func() { results <- evaluate("session-a") }()
	go func() { results <- evaluate("session-b") }()
	ready.Wait()
	close(start)
	firstResult, secondResult := <-results, <-results
	testutil.FailErr(t, "evaluate first concurrent occurrence", firstResult.err)
	testutil.FailErr(t, "evaluate second concurrent occurrence", secondResult.err)
	first, second := firstResult.result, secondResult.result
	byRule := map[string]*PipelineResult{}
	for _, result := range []*PipelineResult{first, second} {
		if result.Decision != nil {
			byRule[result.Decision.Rule] = result
		} else {
			byRule["none"] = result
		}
	}
	if byRule["publisher.a/READ"] == nil || byRule["none"] == nil {
		t.Fatalf("concurrent decisions = %#v, want publisher.a/READ and none", byRule)
	}
}

func TestSelectorProviderFailureUsesRuleOnError(t *testing.T) {
	rule := &Rule{
		ID: "SELECT", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectBlock,
		Enforcement: "enforce", OnError: "fail_open", Selector: Selector{"paintedwolf.session_role": {"coordinator"}},
	}
	pipeline := NewGuardPipeline(NewRuleSet([]*Rule{rule}), nil, nil)
	pipeline.EnableAnchor(AnchorToolPreInvoke)
	gc := NewGuardContext()
	gc.RegisterProvider("paintedwolf.session_role", func(*GuardContext) error { return errors.New("session role unavailable") })
	res, err := pipeline.EvaluateBlock(t.Context(), AnchorToolPreInvoke, gc)
	testutil.FailErr(t, "evaluate selector provider failure", err)
	if res.Decision != nil || len(res.Trace.Entries) != 1 || res.Trace.Entries[0].Outcome != TraceErrored {
		t.Fatalf("result = %#v, want fail-open errored trace", res)
	}
}

func TestCopyProviderFailureUsesRuleOnError(t *testing.T) {
	rule := &Rule{
		ID: "COPY", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectWarn,
		Enforcement: "enforce", OnError: "fail_open", Copy: Copy{Title: "Role {{ paintedwolf.session_role }}"},
	}
	pipeline := NewGuardPipeline(NewRuleSet([]*Rule{rule}), nil, nil)
	pipeline.EnableAnchor(AnchorToolPreInvoke)
	gc := NewGuardContext()
	gc.RegisterProvider("paintedwolf.session_role", func(*GuardContext) error { return errors.New("session role unavailable") })
	res, err := pipeline.EvaluateBlock(t.Context(), AnchorToolPreInvoke, gc)
	testutil.FailErr(t, "evaluate copy provider failure", err)
	if res.Decision != nil || len(res.Trace.Entries) != 1 || res.Trace.Entries[0].Outcome != TraceErrored {
		t.Fatalf("result = %#v, want fail-open errored trace", res)
	}
}

type fixedDetector struct {
	name     string
	findings []Finding
}

func (d fixedDetector) Name() string { return d.name }
func (d fixedDetector) Inspect(*GuardContext) ([]Finding, error) {
	return append([]Finding(nil), d.findings...), nil
}

func TestDetectorFactsAreRuleLocal(t *testing.T) {
	first := &Rule{ID: "A_FIRST", Kind: KindDetector, Anchor: AnchorContentOutput, Effect: EffectWarn, Enforcement: "enforce", Detector: &DetectorRef{Ref: "detector://high"}, When: "jailbreak_score > 0.5"}
	second := &Rule{ID: "B_SECOND", Kind: KindDetector, Anchor: AnchorContentOutput, Effect: EffectWarn, Enforcement: "enforce", Detector: &DetectorRef{Ref: "detector://none"}, When: "jailbreak_score > 0.5"}
	registry := NewDetectorRegistry()
	registry.Register(fixedDetector{name: "high", findings: []Finding{{Fact: "jailbreak_score", Score: 0.9}}})
	registry.Register(fixedDetector{name: "none"})
	loader, err := NewLoader("")
	testutil.FailErr(t, "detector loader", err)
	first = loadDetectorTestRule(t, loader, registry, first, "jailbreak_score")
	second = loadDetectorTestRule(t, loader, registry, second, "jailbreak_score")
	pipeline := NewGuardPipeline(NewRuleSet([]*Rule{first, second}), loader, nil)
	pipeline.SetDetectors(registry)
	pipeline.EnableAnchor(AnchorContentOutput)
	res, err := pipeline.EvaluateBlock(t.Context(), AnchorContentOutput, NewGuardContext())
	testutil.FailErr(t, "evaluate detector rules", err)
	if res.Decision == nil || res.Decision.Code != "A_FIRST" {
		t.Fatalf("decision = %#v, want A_FIRST", res.Decision)
	}
	if len(res.Trace.Entries) != 2 || res.Trace.Entries[1].Outcome != TracePassed {
		t.Fatalf("trace = %#v, want second detector passed", res.Trace.Entries)
	}
}

func TestSelectionObservesProviderWritesAfterAnEarlierSelector(t *testing.T) {
	for _, failure := range []error{nil, errors.New("profile unavailable")} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			rules := NewRuleSet([]*Rule{
				{ID: "A_READ", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectWarn, Enforcement: "enforce", Selector: Selector{"tool": {"read"}}},
				{ID: "B_PROFILE", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectWarn, Enforcement: "enforce", OnError: "fail_open", Selector: Selector{"paintedwolf.session_role": {"implement"}}},
				{ID: "C_GREP", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectWarn, Enforcement: "enforce", Selector: Selector{"tool": {"grep"}}},
				{ID: "D_READ", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectWarn, Enforcement: "enforce", Selector: Selector{"tool": {"read"}}},
			})
			pipeline := NewGuardPipeline(rules, nil, nil)
			pipeline.EnableAnchor(AnchorToolPreInvoke)
			gc := NewGuardContext()
			gc.Invocation.Tool = "read"
			gc.RegisterProvider("paintedwolf.session_role", func(g *GuardContext) error {
				g.Invocation.Tool = "grep"
				g.Session.Role = "implement"
				return failure
			})
			for _, want := range [][]string{{"A_READ", "B_PROFILE", "C_GREP"}, {"A_READ", "B_PROFILE", "D_READ"}} {
				gc.Invocation.Tool = "read"
				result, err := pipeline.EvaluateBlock(t.Context(), AnchorToolPreInvoke, gc)
				testutil.FailErr(t, "evaluate provider-backed selectors", err)
				var got []string
				for _, entry := range result.Trace.Entries {
					got = append(got, entry.Rule)
				}
				if !slices.Equal(got, want) {
					t.Fatalf("selected rules = %v, want %v", got, want)
				}
			}
		})
	}
}
