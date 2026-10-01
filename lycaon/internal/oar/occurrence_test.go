package oar

import (
	"context"
	"github.com/lycaon/lycaon/internal/oarcore"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPublishOccurrenceAnchorUsesCoreSpelling(t *testing.T) {
	ensureCatalog(t)
	gc := NewGuardContext()
	publishOccurrenceAnchor(gc, AnchorContentInput, InstalledCapabilityDocument())
	if gc.Anchor != CoreAnchorModelInput {
		t.Fatalf("anchor fact = %q, want %q", gc.Anchor, CoreAnchorModelInput)
	}
}

func TestNewGuardPipelineWiresLoaderDetectors(t *testing.T) {
	reg := NewDetectorRegistry()
	reg.Register(corpusDetector{name: "error", fails: true})
	l, err := NewLoader("")
	testutil.FailErr(t, "loader", err)
	l.SetDetectors(reg)
	rule := &Rule{
		ID: "DET", Kind: KindDetector, Anchor: AnchorToolPreInvoke,
		Effect: EffectWarn, Enforcement: "enforce", OnError: "fail_open",
		Detector: &DetectorRef{Ref: "detector://error"},
	}
	later := &Rule{
		ID: "LATER", Kind: KindPolicy, Anchor: AnchorToolPreInvoke,
		When: "true", Effect: EffectWarn, Enforcement: "enforce",
	}
	p := NewGuardPipeline(NewRuleSet([]*Rule{rule, later}), l, NewCounterStore())
	p.EnableAnchor(AnchorToolPreInvoke)
	res, err := p.EvaluateBlock(context.Background(), AnchorToolPreInvoke, NewGuardContext())
	testutil.FailErr(t, "eval", err)
	if res.Decision == nil || res.Decision.Code != "LATER" {
		t.Fatalf("decision = %+v, want LATER after fail_open detector", res.Decision)
	}
}

func TestCapabilityFactsAreAvailableToEveryConsumer(t *testing.T) {
	for _, fact := range occurrenceFactDecls {
		name := publishedName(fact.name, fact.tier)
		t.Run(name, func(t *testing.T) {
			expr := name + " == " + name
			if fact.typ == oarcore.TypeListString {
				expr = "size(" + name + ") >= 0"
			}
			if err := checkWhenAgainstSpec(expr, nil); err != nil {
				t.Fatalf("[OAR-FACT-27] declared fact must type-check in when: %v", err)
			}
		})
	}
}

func TestEvaluateConditionDivisionByZeroRaises(t *testing.T) {
	ok, err := EvaluateCondition(`1.0 / 0.0 > 0.5`, NewGuardContext())
	if err == nil {
		t.Fatalf("want raise, got ok=%v", ok)
	}
}

func TestTransformAccumulatesAndApplies(t *testing.T) {
	ensureCatalog(t)
	l, err := NewLoader("")
	testutil.FailErr(t, "loader", err)
	l.SetCapability(&CapabilityDocument{
		Version: "1.0",
		Anchors: AnchorMap{Core: map[string]string{
			CoreAnchorToolPreInvoke:   AnchorToolPreInvoke,
			CoreAnchorToolHandler:     AnchorToolHandler,
			CoreAnchorToolPostInvoke:  AnchorToolPost,
			CoreAnchorAgentPostTurn:   AnchorCoordinatorPostTurn,
			CoreAnchorAgentFinalize:   AnchorWorkerFinalize,
			CoreAnchorModelInput:      AnchorContentInput,
			CoreAnchorModelOutput:     AnchorContentOutput,
			CoreAnchorModelToolResult: AnchorContentToolResult,
		}},
		Profiles:          []string{},
		SupportsTransform: true,
	})
	rule := &Rule{
		ID: "REDACT", Kind: KindPolicy, Anchor: AnchorContentOutput,
		When: "true", Effect: EffectTransform, Enforcement: "enforce",
		Transform: &TransformSpec{Action: "redact", Target: "content", HasReplacement: true, Replacement: "X"},
	}
	p := NewGuardPipeline(NewRuleSet([]*Rule{rule}), l, NewCounterStore())
	p.EnableAnchor(AnchorContentOutput)
	gc := NewGuardContext()
	gc.Content = "secret"
	gc.ContentSet = true
	res, err := p.EvaluateBlock(context.Background(), AnchorContentOutput, gc)
	testutil.FailErr(t, "eval", err)
	if res.Decision == nil || res.Decision.Effect != EffectTransform {
		t.Fatalf("decision = %+v", res.Decision)
	}
	if res.Content != "X" {
		t.Fatalf("content = %q, want X", res.Content)
	}
}
