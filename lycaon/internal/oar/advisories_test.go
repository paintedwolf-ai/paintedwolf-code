package oar

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func advisoryRule(id string, effect Effect, anchor string) *Rule {
	return &Rule{
		OAR: "1.0", ID: id, Kind: KindPolicy,
		Anchor: anchor, Effect: effect, Enforcement: "enforce",
		OnError: "fail_closed",
		Copy:    Copy{What: id + " what", Fix: id + " fix"},
	}
}

func evaluateAdvisories(t *testing.T, rules []*Rule, anchor string, gc *GuardContext) *PipelineResult {
	t.Helper()
	ensureCatalog(t)
	l, err := NewLoader(schemaDir(t))
	testutil.FailErr(t, "loader", err)
	p := NewGuardPipeline(NewRuleSet(rules), l, NewCounterStore())
	p.EnableAnchor(anchor)
	if gc == nil {
		gc = NewGuardContext()
	}
	res, err := p.EvaluateBlock(context.Background(), anchor, gc)
	testutil.FailErr(t, "EvaluateBlock", err)
	return res
}

func TestAdvisoriesAccumulateInEvaluationOrder(t *testing.T) {
	res := evaluateAdvisories(t, []*Rule{
		advisoryRule("A_SPEND", EffectNudge, AnchorCoordinatorPostTurn),
		advisoryRule("B_GROUND", EffectNudge, AnchorCoordinatorPostTurn),
	}, AnchorCoordinatorPostTurn, nil)
	if res.Decision == nil || res.Decision.Effect != EffectNudge || res.Decision.Code != "A_SPEND" {
		t.Fatalf("decision = %#v", res.Decision)
	}
	got := make([]string, len(res.Decision.Advisories))
	for i, a := range res.Decision.Advisories {
		got[i] = a.Code
		if a.Copy["what"] != a.Code+" what" {
			t.Fatalf("advisory %s copy = %#v", a.Code, a.Copy)
		}
	}
	if len(got) != 2 || got[0] != "A_SPEND" || got[1] != "B_GROUND" {
		t.Fatalf("advisories = %v, want [A_SPEND B_GROUND]", got)
	}
}

func TestAdvisoriesWarnsAccumulate(t *testing.T) {
	res := evaluateAdvisories(t, []*Rule{
		advisoryRule("A_NOTE", EffectWarn, AnchorToolPost),
		advisoryRule("B_NOTE", EffectWarn, AnchorToolPost),
	}, AnchorToolPost, nil)
	if res.Decision == nil || res.Decision.Effect != EffectWarn || res.Decision.Code != "A_NOTE" {
		t.Fatalf("decision = %#v", res.Decision)
	}
	if len(res.Decision.Advisories) != 2 || res.Decision.Advisories[0].Code != "A_NOTE" || res.Decision.Advisories[1].Code != "B_NOTE" {
		t.Fatalf("advisories = %#v", res.Decision.Advisories)
	}
}

func TestAdvisoriesOmitASuppressedRule(t *testing.T) {
	specific := advisoryRule("A_SPECIFIC", EffectNudge, AnchorCoordinatorPostTurn)
	specific.Overrides = []string{"B_GENERAL"}
	specific.overrideTargets = []string{"B_GENERAL"}
	general := advisoryRule("B_GENERAL", EffectNudge, AnchorCoordinatorPostTurn)
	res := evaluateAdvisories(t, []*Rule{specific, general}, AnchorCoordinatorPostTurn, nil)
	if res.Decision == nil || res.Decision.Code != "A_SPECIFIC" {
		t.Fatalf("decision = %#v", res.Decision)
	}
	if len(res.Decision.Advisories) != 1 || res.Decision.Advisories[0].Code != "A_SPECIFIC" {
		t.Fatalf("advisories = %#v, want only A_SPECIFIC", res.Decision.Advisories)
	}
}

func TestAdvisoriesOmitAMonitorRule(t *testing.T) {
	watch := advisoryRule("B_WATCH", EffectNudge, AnchorCoordinatorPostTurn)
	watch.Enforcement = "monitor"
	res := evaluateAdvisories(t, []*Rule{
		advisoryRule("A_ENFORCE", EffectNudge, AnchorCoordinatorPostTurn),
		watch,
	}, AnchorCoordinatorPostTurn, nil)
	if res.Decision == nil || res.Decision.Code != "A_ENFORCE" {
		t.Fatalf("decision = %#v", res.Decision)
	}
	if len(res.Decision.Advisories) != 1 || res.Decision.Advisories[0].Code != "A_ENFORCE" {
		t.Fatalf("advisories = %#v, want only A_ENFORCE", res.Decision.Advisories)
	}
	found := false
	for _, e := range res.Trace.Entries {
		if e.Rule == "B_WATCH" && e.Outcome == TraceMonitoredFired {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected monitored_fired for B_WATCH, got %#v", res.Trace.Entries)
	}
}

func TestAdvisoriesEmptyWhenTransformWins(t *testing.T) {
	transform := advisoryRule("C_TRANSFORM", EffectTransform, AnchorContentOutput)
	transform.Transform = &TransformSpec{Action: "annotate", Target: "content", Replacement: "reviewed", HasReplacement: true}
	gc := NewGuardContext()
	gc.Content = "hello"
	gc.ContentSet = true
	res := evaluateAdvisories(t, []*Rule{
		advisoryRule("A_WARN", EffectWarn, AnchorContentOutput),
		advisoryRule("B_NUDGE", EffectNudge, AnchorContentOutput),
		transform,
	}, AnchorContentOutput, gc)
	if res.Decision == nil || res.Decision.Effect != EffectTransform || res.Decision.Code != "C_TRANSFORM" {
		t.Fatalf("decision = %#v", res.Decision)
	}
	if len(res.Decision.Advisories) != 0 {
		t.Fatalf("advisories = %#v, want empty", res.Decision.Advisories)
	}
}

func TestRendererWalksAdvisories(t *testing.T) {
	r := NewRenderer(stubFmt{}, stubFmt{})
	rendered, err := r.Render(context.Background(), StagePostTurn, &Decision{
		Effect: EffectNudge,
		Code:   "A_SPEND",
		Advisories: []Advisory{
			{Code: "A_SPEND"},
			{Code: "B_GROUND"},
		},
	})
	testutil.FailErr(t, "render", err)
	if len(rendered) != 2 {
		t.Fatalf("rendered = %#v, want 2", rendered)
	}
	if rendered[0].Decision.Code != "A_SPEND" || rendered[1].Decision.Code != "B_GROUND" {
		t.Fatalf("order = %s then %s", rendered[0].Decision.Code, rendered[1].Decision.Code)
	}
	if rendered[0].Channel != ChannelGuidanceNudge || rendered[1].Channel != ChannelGuidanceNudge {
		t.Fatalf("channels = %s, %s", rendered[0].Channel, rendered[1].Channel)
	}
	if rendered[0].Text == "" || rendered[1].Text == "" {
		t.Fatalf("missing rendered text: %#v", rendered)
	}
}
