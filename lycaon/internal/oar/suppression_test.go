package oar

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// suppressionRule builds a minimal rule for the ordering/suppression algebra.
// These tests mirror the published corpus's baseline suppression scenarios.
func suppressionRule(id string, kind Kind, effect Effect, overrides ...string) *Rule {
	return &Rule{
		OAR: "1.0", ID: id, Kind: kind, Effect: effect,
		Anchor: "tool.pre_invoke", Enforcement: "enforce", Overrides: overrides,
	}
}

// [OAR-EVAL-18] a suppressor evaluates before the rule it names, even when the
// base kind order puts the named rule first. Mirrors the corpus fixture where a
// schema rule would otherwise precede the policy rule that overrides it.
func TestSuppressorEvaluatesBeforeTheRuleItNames(t *testing.T) {
	general := suppressionRule("A_GENERAL", KindSchema, EffectNudge)
	specific := suppressionRule("B_SPECIFIC", KindPolicy, EffectWarn, "A_GENERAL")
	rs := NewRuleSet([]*Rule{general, specific})
	if err := ResolveOverrides(rs); err != nil {
		t.Fatalf("ResolveOverrides: %v", err)
	}

	ordered := orderBySuppression(rs.All())
	if len(ordered) != 2 || ordered[0].ID != "B_SPECIFIC" || ordered[1].ID != "A_GENERAL" {
		var got []string
		for _, r := range ordered {
			got = append(got, r.ID)
		}
		t.Fatalf("order = %v, want [B_SPECIFIC A_GENERAL]", got)
	}
}

// A rule no selected rule suppresses keeps the base [OAR-EVAL-1] order.
func TestOrderUnchangedWithoutSuppression(t *testing.T) {
	rs := NewRuleSet([]*Rule{
		suppressionRule("B", KindPolicy, EffectWarn),
		suppressionRule("A", KindSchema, EffectNudge),
	})
	if err := ResolveOverrides(rs); err != nil {
		t.Fatalf("ResolveOverrides: %v", err)
	}
	ordered := orderBySuppression(rs.All())
	if ordered[0].ID != "A" || ordered[1].ID != "B" {
		t.Fatalf("order = %s,%s want A,B", ordered[0].ID, ordered[1].ID)
	}
}

// [OAR-EVAL-15] suppression does not transit: A overriding B does not suppress
// what B overrides. Ordering still places each suppressor ahead of its target.
func TestSuppressionDoesNotTransit(t *testing.T) {
	rs := NewRuleSet([]*Rule{
		suppressionRule("A", KindSchema, EffectWarn, "B"),
		suppressionRule("B", KindPolicy, EffectWarn, "C"),
		suppressionRule("C", KindInvariant, EffectWarn),
	})
	if err := ResolveOverrides(rs); err != nil {
		t.Fatalf("ResolveOverrides: %v", err)
	}
	a, _ := rs.Get("A")
	if len(a.overrideTargets) != 1 || a.overrideTargets[0] != "B" {
		t.Fatalf("A targets = %v, want [B] — no transitive closure", a.overrideTargets)
	}
	ordered := orderBySuppression(rs.All())
	pos := map[string]int{}
	for i, r := range ordered {
		pos[r.ID] = i
	}
	if !(pos["A"] < pos["B"] && pos["B"] < pos["C"]) {
		t.Fatalf("order = %v, want A before B before C", pos)
	}
}

// [OAR-EVAL-13] an entry resolving to no loaded rule is refused, naming it.
func TestOverridesUnknownTargetRefused(t *testing.T) {
	rs := NewRuleSet([]*Rule{suppressionRule("A", KindPolicy, EffectWarn, "NOPE")})
	err := ResolveOverrides(rs)
	if err == nil || !strings.Contains(err.Error(), "NOPE") {
		t.Fatalf("err = %v, want a refusal naming NOPE", err)
	}
}

// [OAR-EVAL-17] a mandatory rule cannot be suppressed, and the set is refused at
// load rather than the override silently doing nothing.
func TestOverridesMandatoryRefused(t *testing.T) {
	floor := suppressionRule("FLOOR", KindInvariant, EffectBlock)
	floor.Mandatory = true
	rs := NewRuleSet([]*Rule{floor, suppressionRule("A", KindPolicy, EffectWarn, "FLOOR")})
	err := ResolveOverrides(rs)
	if err == nil || !strings.Contains(err.Error(), "mandatory") {
		t.Fatalf("err = %v, want a mandatory refusal", err)
	}
}

// [OAR-EVAL-16] a cycle is refused at load, naming the rules in it — without
// this, [OAR-EVAL-18]'s ordering has no solution.
func TestOverridesCycleRefused(t *testing.T) {
	rs := NewRuleSet([]*Rule{
		suppressionRule("A", KindPolicy, EffectWarn, "B"),
		suppressionRule("B", KindPolicy, EffectWarn, "A"),
	})
	err := ResolveOverrides(rs)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("err = %v, want a cycle refusal", err)
	}
}

// [OAR-EVAL-14] end to end: the suppressed rule contributes no decision and no
// side-effect, and is still traced. This is the corpus's own scenario — a schema
// nudge with increment_counter, suppressed by a policy warn with
// increment_breaker — driven through the production pipeline.
func TestSuppressedRuleContributesNothingButIsTraced(t *testing.T) {
	ensureCatalog(t)
	l, err := NewLoader("")
	testutil.FailErr(t, "loader", err)

	general := suppressionRule("A_GENERAL", KindSchema, EffectNudge)
	general.Anchor = AnchorToolHandler
	general.OnFire = []OnFireAction{OnFireIncrementCounter}
	specific := suppressionRule("B_SPECIFIC", KindPolicy, EffectWarn, "A_GENERAL")
	specific.Anchor = AnchorToolHandler
	specific.OnFire = []OnFireAction{OnFireIncrementBreaker}

	rs := NewRuleSet([]*Rule{general, specific})
	testutil.FailErr(t, "ResolveOverrides", ResolveOverrides(rs))

	counters := NewCounterStore()
	p := NewGuardPipeline(rs, l, counters)
	p.EnableAnchor(AnchorToolHandler)

	gc := NewGuardContext()
	gc.Session.SessionID = "s1"
	gc.Invocation.Tool = "grep"
	res, err := p.EvaluateBlock(context.Background(), AnchorToolHandler, gc)
	testutil.FailErr(t, "EvaluateBlock", err)

	if res.Decision == nil || res.Decision.Code != "B_SPECIFIC" {
		t.Fatalf("decision = %#v, want only B_SPECIFIC", res.Decision)
	}
	var got []string
	for _, e := range res.Trace.Entries {
		got = append(got, e.Rule+":"+string(e.Outcome))
	}
	if len(got) != 2 || got[0] != "B_SPECIFIC:fired" || got[1] != "A_GENERAL:suppressed" {
		t.Fatalf("trace = %v, want [B_SPECIFIC:fired A_GENERAL:suppressed]", got)
	}
	if n := counters.Get("s1", "A_GENERAL", CounterFire); n != 0 {
		t.Fatalf("suppressed rule applied its side-effect: repeat_count = %d", n)
	}
}

// [OAR-EVAL-1] ties break on the qualified identifier, not the bare id. Two
// publishers shipping the same bare id must still order deterministically by
// publisher — sorting on the bare id leaves their relative order to chance.
func TestOrderBreaksTiesOnQualifiedIdentifier(t *testing.T) {
	zebra := suppressionRule("SAME", KindPolicy, EffectWarn)
	zebra.Namespace = "zebra"
	acme := suppressionRule("SAME", KindPolicy, EffectWarn)
	acme.Namespace = "acme"

	// Input order is the reverse of the answer, so a stable sort that ignored the
	// namespace would leave zebra first.
	ordered := NewRuleSet([]*Rule{zebra, acme}).All()
	if len(ordered) != 2 || ordered[0].Qualified() != "acme/SAME" || ordered[1].Qualified() != "zebra/SAME" {
		var got []string
		for _, r := range ordered {
			got = append(got, r.Qualified())
		}
		t.Fatalf("order = %v, want [acme/SAME zebra/SAME]", got)
	}
}

// A suppressor names one publisher's rule; a same-bare-id rule from another
// publisher keeps evaluating.
func TestSuppressionIsPerPublisher(t *testing.T) {
	mine := suppressionRule("SHARED", KindSchema, EffectNudge)
	mine.Namespace = "acme"
	theirs := suppressionRule("SHARED", KindSchema, EffectNudge)
	theirs.Namespace = "zebra"
	sup := suppressionRule("SUP", KindPolicy, EffectWarn, "acme/SHARED")
	sup.Namespace = "acme"

	rs := NewRuleSet([]*Rule{mine, theirs, sup})
	testutil.FailErr(t, "ResolveOverrides", ResolveOverrides(rs))

	s, _ := rs.Get("acme/SUP")
	if len(s.overrideTargets) != 1 || s.overrideTargets[0] != "acme/SHARED" {
		t.Fatalf("targets = %v, want [acme/SHARED]", s.overrideTargets)
	}
}

// [OAR-EVAL-13] a bare entry reads in the suppressing rule's own namespace; a
// same-id rule in another publisher's namespace is not the target.
func TestBareOverrideResolvesInOwnNamespace(t *testing.T) {
	mine := suppressionRule("TARGET", KindSchema, EffectWarn)
	mine.Namespace = "acme"
	theirs := suppressionRule("OTHER", KindPolicy, EffectWarn, "TARGET")
	theirs.Namespace = "acme"
	rs := NewRuleSet([]*Rule{mine, theirs})
	if err := ResolveOverrides(rs); err != nil {
		t.Fatalf("same-namespace bare entry must resolve: %v", err)
	}

	foreign := suppressionRule("OTHER", KindPolicy, EffectWarn, "TARGET")
	foreign.Namespace = "other"
	rs2 := NewRuleSet([]*Rule{mine, foreign})
	if err := ResolveOverrides(rs2); err == nil {
		t.Fatal("a bare entry must not reach another namespace's rule")
	}
}
