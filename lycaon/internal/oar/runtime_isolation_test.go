package oar

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestConcurrentEvaluationsKeepRulesSessionAndSnapshotTogether(t *testing.T) {
	countA := &Rule{ID: "COUNT", Namespace: "publisher.a", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectAllow, Enforcement: "enforce"}
	readA := &Rule{ID: "READ", Namespace: "publisher.a", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectBlock, Enforcement: "enforce", When: `paintedwolf.workers_idle && fire_count_of("COUNT") == 1`}
	countB := &Rule{ID: "COUNT", Namespace: "publisher.b", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectAllow, Enforcement: "enforce"}
	readB := &Rule{ID: "READ", Namespace: "publisher.b", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectBlock, Enforcement: "enforce", When: `paintedwolf.workers_idle && fire_count_of("COUNT") == 1`}
	rulesA := NewRuleSet([]*Rule{countA, readA})
	rulesB := NewRuleSet([]*Rule{countB, readB})
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
	pipeline.Counters().Increment("session-a", "publisher.a/COUNT", CounterFire, 1)

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
		Enforcement: "enforce", OnError: "fail_open", Selector: Selector{"paintedwolf.profile": {"coordinator"}},
	}
	pipeline := NewGuardPipeline(NewRuleSet([]*Rule{rule}), nil, nil)
	pipeline.EnableAnchor(AnchorToolPreInvoke)
	gc := NewGuardContext()
	gc.RegisterProvider("paintedwolf.profile", func(*GuardContext) error { return errors.New("profile unavailable") })
	res, err := pipeline.EvaluateBlock(t.Context(), AnchorToolPreInvoke, gc)
	testutil.FailErr(t, "evaluate selector provider failure", err)
	if res.Decision != nil || len(res.Trace.Entries) != 1 || res.Trace.Entries[0].Outcome != TraceErrored {
		t.Fatalf("result = %#v, want fail-open errored trace", res)
	}
}

func TestCopyProviderFailureUsesRuleOnError(t *testing.T) {
	rule := &Rule{
		ID: "COPY", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectWarn,
		Enforcement: "enforce", OnError: "fail_open", Copy: Copy{Title: "Profile {{ paintedwolf.profile }}"},
	}
	pipeline := NewGuardPipeline(NewRuleSet([]*Rule{rule}), nil, nil)
	pipeline.EnableAnchor(AnchorToolPreInvoke)
	gc := NewGuardContext()
	gc.RegisterProvider("paintedwolf.profile", func(*GuardContext) error { return errors.New("profile unavailable") })
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
	first = loadDetectorTestRule(t, loader, registry, first, "jailbreak")
	second = loadDetectorTestRule(t, loader, registry, second, "jailbreak")
	pipeline := NewGuardPipeline(NewRuleSet([]*Rule{first, second}), loader, nil)
	pipeline.SetDetectors(registry)
	pipeline.EnableAnchor(AnchorContentOutput)
	res, err := pipeline.EvaluateBlock(t.Context(), AnchorContentOutput, NewGuardContext())
	testutil.FailErr(t, "evaluate detector rules", err)
	if len(res.Decision.Advisories) != 1 || res.Decision.Advisories[0].Code != "A_FIRST" {
		t.Fatalf("advisories = %#v, want only A_FIRST", res.Decision.Advisories)
	}
	if len(res.Trace.Entries) != 2 || res.Trace.Entries[1].Outcome != TracePassed {
		t.Fatalf("trace = %#v, want second detector passed", res.Trace.Entries)
	}
}

func TestAccumulatedTransformsKeepEachRulesDetectorFacts(t *testing.T) {
	first := &Rule{
		ID: "A_FIRST", Kind: KindDetector, Anchor: AnchorContentOutput, Effect: EffectTransform, Enforcement: "enforce",
		Detector:  &DetectorRef{Ref: "detector://spans"},
		Transform: &TransformSpec{Action: "replace", Target: "secret_matches", Replacement: "X", HasReplacement: true},
	}
	second := &Rule{
		ID: "B_SECOND", Kind: KindDetector, Anchor: AnchorContentOutput, Effect: EffectTransform, Enforcement: "enforce",
		Detector:  &DetectorRef{Ref: "detector://none"},
		Transform: &TransformSpec{Action: "replace", Target: "secret_matches", Replacement: "Y", HasReplacement: true},
	}
	registry := NewDetectorRegistry()
	registry.Register(fixedDetector{name: "spans", findings: []Finding{{
		Fact: "secret_matches", Value: []any{map[string]any{"start": 0, "end": 1}},
	}}})
	registry.Register(fixedDetector{name: "none"})
	pipeline := NewGuardPipeline(NewRuleSet([]*Rule{first, second}), nil, nil)
	pipeline.SetDetectors(registry)
	pipeline.EnableAnchor(AnchorContentOutput)
	gc := NewGuardContext()
	gc.SetContentSegments([]ContentSegment{{Content: "abcdef"}})
	res, err := pipeline.EvaluateBlock(t.Context(), AnchorContentOutput, gc)
	testutil.FailErr(t, "evaluate detector transforms", err)
	if res.Content != "Xbcdef" {
		t.Fatalf("content = %q, want Xbcdef", res.Content)
	}
	if len(res.Transforms) != 2 || res.Transforms[0].Target != "secret_matches" || res.Transforms[1].Target != "secret_matches" {
		t.Fatalf("reported transforms = %#v, want original portable targets", res.Transforms)
	}
}

type failingEventPublisher struct{}

func (failingEventPublisher) Publish(context.Context, OnFireEvent) error {
	return errors.New("event stream refused write")
}

func TestPublishEventFailureUsesOnErrorSubstituteCopy(t *testing.T) {
	fallback := &Rule{ID: "FALLBACK", Namespace: "publisher", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectNudge, Enforcement: "enforce", Copy: Copy{Title: "Fallback"}}
	primary := &Rule{ID: "PRIMARY", Namespace: "publisher", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectWarn, Enforcement: "enforce", OnError: "FALLBACK", OnFire: []OnFireAction{OnFirePublishEvent}}
	rules := NewRuleSet([]*Rule{primary, fallback})
	testutil.FailErr(t, "resolve error substitute", RejectUnknownErrorSubstitutes(rules))
	pipeline := NewGuardPipeline(rules, nil, nil)
	pipeline.SetEventPublisher(failingEventPublisher{})
	pipeline.EnableAnchor(AnchorToolPreInvoke)
	res, err := pipeline.EvaluateBlock(t.Context(), AnchorToolPreInvoke, NewGuardContext())
	testutil.FailErr(t, "evaluate event failure", err)
	if res.Decision == nil || res.Decision.Rule != "publisher/FALLBACK" || res.Decision.Copy["title"] != "Fallback" {
		t.Fatalf("decision = %#v, want substituted fallback with copy", res.Decision)
	}
}

func TestCounterReferenceScannerIgnoresFunctionTextInsideStrings(t *testing.T) {
	refs, err := oarcore.CounterReferences(`contains("fire_count_of(\"MISSING\")", "count") && fire_count_of("REAL") == 0`, "")
	testutil.FailErr(t, "parse counter references", err)
	if len(refs) != 1 || refs[0].Literal != "REAL" {
		t.Fatalf("counter references = %#v, want only REAL", refs)
	}
}
