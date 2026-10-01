package oar

import (
	"github.com/lycaon/lycaon/internal/oarcore"
	"strings"
	"testing"
)

// The three behaviours [OAR-FIRE-11] requires of a counter read, mirroring the
// conformance fixtures fire-count-of-escalates-across-occurrences,
// fire-count-of-non-literal-argument-rejected, and
// fire-count-of-unresolvable-reference-rejected.

func TestCounterReadRequiresStringLiteral(t *testing.T) {
	for _, expr := range []string{
		`fire_count_of(tool) > 0`,
		`breaker_count_of(tool) > 0`,
		`fire_count_of("A" + "B") > 0`,
	} {
		if _, err := oarcore.CounterReferences(expr, ""); err == nil {
			t.Fatalf("expected rejection for %q", expr)
		}
	}
}

func TestCounterReadAcceptsLiteral(t *testing.T) {
	for _, expr := range []string{
		`fire_count_of("TALLY") >= 2`,
		`breaker_count_of('TALLY') >= 5`,
		`fire_count_of("TALLY") >= 2 && fire_count_of("TALLY") < 5`,
	} {
		if _, err := oarcore.CounterReferences(expr, ""); err != nil {
			t.Fatalf("%q: %v", expr, err)
		}
	}
}

func TestCounterReadIgnoresLongerIdentifier(t *testing.T) {
	// A host fact whose name ends in one of these must not be scanned as one.
	if _, err := oarcore.CounterReferences(`my_fire_count_of(tool) > 0`, ""); err != nil {
		t.Fatalf("unrelated identifier scanned: %v", err)
	}
}

func TestCounterReadMustNameALoadedRule(t *testing.T) {
	rs := NewRuleSet([]*Rule{
		{ID: "TALLY", Namespace: "oar.test", Anchor: "tool.pre_invoke", Kind: "schema", Effect: EffectAllow},
		{ID: "READER", Namespace: "oar.test", Anchor: "tool.pre_invoke", Kind: "invariant", Effect: EffectWarn,
			When: `fire_count_of("NO_SUCH_RULE") > 0`},
	})
	err := RequireResolvableCounterReads(rs)
	if err == nil {
		t.Fatal("expected rejection for an unresolvable rule id")
	}
	if !strings.Contains(err.Error(), "NO_SUCH_RULE") {
		t.Fatalf("error must name the missing id, got %v", err)
	}
}

func TestCounterReadResolvesSiblingAndQualified(t *testing.T) {
	rs := NewRuleSet([]*Rule{
		{ID: "TALLY", Namespace: "oar.test", Anchor: "tool.pre_invoke", Kind: "schema", Effect: EffectAllow},
		{ID: "BARE", Namespace: "oar.test", Anchor: "tool.pre_invoke", Kind: "invariant", Effect: EffectWarn,
			When: `fire_count_of("TALLY") >= 2`},
		{ID: "QUALIFIED", Namespace: "oar.test", Anchor: "tool.pre_invoke", Kind: "invariant", Effect: EffectBlock,
			When: `breaker_count_of("oar.test/TALLY") >= 5`},
	})
	if err := RequireResolvableCounterReads(rs); err != nil {
		t.Fatalf("sibling reference rejected: %v", err)
	}
}

func TestCounterReadEvaluatesAnotherRulesCounter(t *testing.T) {
	store := NewCounterStore()
	gc := NewGuardContext()
	gc.SessionID = "s1"
	holder := newEvalHolder(gc, nil, store)

	if got := holder.counterOf("TALLY", CounterFire); got != 0 {
		t.Fatalf("unfired rule must read 0, got %d", got)
	}
	store.Increment("s1", "TALLY", CounterFire, 3)
	holder = newEvalHolder(gc, nil, store)
	if got := holder.counterOf("TALLY", CounterFire); got != 3 {
		t.Fatalf("fire_count_of = %d, want 3", got)
	}
	if got := holder.counterOf("OTHER", CounterFire); got != 0 {
		t.Fatalf("another rule's counter leaked: %d", got)
	}
	store.Increment("s1", "TALLY", CounterBreaker, 5)
	holder = newEvalHolder(gc, nil, store)
	if got := holder.counterOf("TALLY", CounterBreaker); got != 5 {
		t.Fatalf("breaker_count_of = %d, want 5", got)
	}
}

func TestCounterReadWithoutSessionOrStoreIsZero(t *testing.T) {
	holder := &evalHolder{}
	holder.set(NewGuardContext()) // no SessionID, no store
	if got := holder.counterOf("TALLY", CounterFire); got != 0 {
		t.Fatalf("want 0 without a session, got %d", got)
	}
}
