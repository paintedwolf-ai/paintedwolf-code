package casegen

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/independence"
)

// toyProducer evaluates `a && (b || x == "p")` over three inputs; fact c has
// no input, and the guard is only delivered to the coordinator.
type toyProducer struct{ runs int }

var toyKnobs = []Knob{
	{Name: "a", Facts: []string{"a"}, Values: []any{false, true}},
	{Name: "b", Facts: []string{"b"}, Values: []any{false, true}},
	{Name: "x", Facts: []string{"x"}, Values: []any{"", "p", "q"}, Labels: []string{"empty", "", ""}},
}

func (p *toyProducer) Knobs(_, recipient string) []Knob {
	if recipient != "coordinator" {
		return nil
	}
	return toyKnobs
}

func (p *toyProducer) Run(_ context.Context, site, _, _ string, knobs []Knob, s Setting) ([]Vector, error) {
	p.runs++
	if site == "hidden" {
		return []Vector{{}}, nil
	}
	a, b, x := s.Value(knobs[0]).(bool), s.Value(knobs[1]).(bool), s.Value(knobs[2]).(string)
	truth := func(v bool) independence.Truth {
		if v {
			return independence.True
		}
		return independence.False
	}
	return []Vector{{
		Truths:    map[string]independence.Truth{"a": truth(a), "b": truth(b), `x == "p"`: truth(x == "p"), "c": independence.False},
		Decision:  a && (b || x == "p"),
		Evaluated: true,
	}}, nil
}

func toyRefs(atom string) []string {
	if atom == `x == "p"` {
		return []string{"x"}
	}
	return []string{atom}
}

func toyDecide(v map[string]bool) bool { return v["a"] && (v["b"] || v[`x == "p"`]) }

func TestGenerateFindsAPairPerInputAndExplainsMisses(t *testing.T) {
	inputs := []string{"a", "b", `x == "p"`}
	var targets []Target
	for _, in := range inputs {
		targets = append(targets, Target{Site: "g", Recipient: "coordinator", Input: in, Needs: Needs(inputs, in, toyDecide, 8), References: toyRefs})
	}
	targets = append(targets,
		Target{Site: "g", Recipient: "worker", Input: "a", Needs: Needs(inputs, "a", toyDecide, 8), References: toyRefs},
		Target{Site: "g", Recipient: "coordinator", Input: "c", Needs: []map[string]independence.Truth{{"a": independence.True}}, References: toyRefs},
		Target{Site: "hidden", Recipient: "coordinator", Input: "a", Needs: Needs(inputs, "a", toyDecide, 8), References: toyRefs},
	)
	p := &toyProducer{}
	outcomes, err := Generate(t.Context(), p, targets, Budget{PerTarget: 40, Total: 400})
	contractcheck.FailErr(t, "Generate failed", err)
	for _, o := range outcomes[:3] {
		if !o.Found || o.With == nil || o.Then == nil {
			t.Fatalf("%s: no pair: %s", o.Input, o.Reason)
		}
		if o.With.Vector == o.Then.Vector || !strings.HasPrefix(o.With.ID, "generated:coordinator:g{") {
			t.Fatalf("%s: pair %+v / %+v", o.Input, o.With, o.Then)
		}
	}
	for i, want := range []string{"no production path delivers", "no producer input drives [c]", "did not evaluate the guard"} {
		if o := outcomes[3+i]; o.Found || !strings.Contains(o.Reason, want) {
			t.Fatalf("outcome %d = %+v, want reason %q", 3+i, o, want)
		}
	}
	if p.runs >= 400 {
		t.Fatalf("runs = %d, want shared memoised runs", p.runs)
	}
}

func TestGenerateStopsAtTheBudget(t *testing.T) {
	inputs := []string{"a", "b"}
	p := &toyProducer{}
	outcomes, err := Generate(t.Context(), p, []Target{{
		Site: "g", Recipient: "coordinator", Input: "b",
		Needs:      []map[string]independence.Truth{{"a": independence.True}},
		References: toyRefs,
	}, {
		Site: "g2", Recipient: "coordinator", Input: "a",
		Needs: Needs(inputs, "a", toyDecide, 4), References: toyRefs,
	}}, Budget{PerTarget: 1, Total: 1})
	contractcheck.FailErr(t, "Generate failed", err)
	if p.runs != 1 || outcomes[0].Found || outcomes[1].Found {
		t.Fatalf("runs=%d outcomes=%+v", p.runs, outcomes)
	}
}

func TestNeedsListsOnlyAssignmentsWhereTheInputDecides(t *testing.T) {
	got := Needs([]string{"a", "b"}, "a", func(v map[string]bool) bool { return v["a"] && v["b"] }, 8)
	if len(got) != 1 || got[0]["b"] != independence.True {
		t.Fatalf("needs = %v", got)
	}
}

func TestLabelAndVectorPrintTheCase(t *testing.T) {
	if got := Label(toyKnobs, Setting{"a": 1, "x": 0}); got != "{a=true}" {
		t.Fatalf("label = %s", got)
	}
	if got := Label(toyKnobs, Setting{"x": 1}); got != "{x=p}" {
		t.Fatalf("label = %s", got)
	}
	v := Vector{Truths: map[string]independence.Truth{"b": independence.False, "a": independence.True}, Decision: true, Evaluated: true}
	if v.String() != "{a=T, b=F} -> true" || (Vector{}).String() != "not evaluated" {
		t.Fatalf("vector = %s", v)
	}
}

// toyLogic reads atoms of the forms `x == "lit"`, `x != "lit"`, a bare fact
// or `!fact`.
type toyLogic struct{ complete bool }

func (toyLogic) References(atom string) []string {
	if f, _, ok := strings.Cut(atom, " "); ok {
		return []string{f}
	}
	return []string{strings.TrimPrefix(atom, "!")}
}

func (toyLogic) Eval(atom string, facts map[string]any) independence.Truth {
	if f, rest, ok := strings.Cut(atom, " == "); ok {
		v, _ := facts[f].(string)
		return boolTruth(fmt.Sprintf("%q", v) == rest)
	}
	if f, rest, ok := strings.Cut(atom, " != "); ok {
		v, _ := facts[f].(string)
		return boolTruth(fmt.Sprintf("%q", v) != rest)
	}
	if f, ok := strings.CutPrefix(atom, "!"); ok {
		v, _ := facts[f].(bool)
		return boolTruth(!v)
	}
	v, ok := facts[atom].(bool)
	if !ok {
		return independence.Unknown
	}
	return boolTruth(v)
}

func (l toyLogic) Domain(string, []string) ([]any, bool) {
	return []any{"", "p", "q", "other"}, l.complete
}

func (toyLogic) Zero(string) any { return false }

func boolTruth(b bool) independence.Truth {
	if b {
		return independence.True
	}
	return independence.False
}

func TestSharedFactJointsRuleOutUnrealisableVectors(t *testing.T) {
	atoms := []string{`x == "p"`, `x == "q"`, `x == "p"`, "y"}
	joints := SharedFactJoints(atoms, toyLogic{complete: true})
	if len(joints) != 1 || !slices.Equal(joints[0].Atoms, []string{`x == "p"`, `x == "q"`}) {
		t.Fatalf("joints = %+v", joints)
	}
	tt := []independence.Truth{independence.True, independence.True}
	if slices.ContainsFunc(joints[0].Allowed, func(v []independence.Truth) bool { return slices.Equal(v, tt) }) || len(joints[0].Allowed) != 3 {
		t.Fatalf("allowed = %v", joints[0].Allowed)
	}
	if got := SharedFactJoints(atoms, toyLogic{}); len(got) != 0 {
		t.Fatalf("incomplete domain gave %+v", got)
	}
	if got := SharedFactJoints([]string{`x == "p"`, `x != "q"`, `z == "p"`}, toyLogic{complete: true}); len(got) != 1 {
		t.Fatalf("joints = %+v", got)
	}
}

func TestFixedFactsAndZeroUnlessFollowTheProducer(t *testing.T) {
	fixed := FixedFacts([]string{`role == "worker"`, "leg", "other"}, toyLogic{}, map[string]any{"role": "worker", "leg": false}, "partition")
	if len(fixed) != 2 || fixed[0].Value != independence.True || fixed[1].Value != independence.False || fixed[0].Source != "partition" {
		t.Fatalf("fixed = %+v", fixed)
	}
	joints := ZeroUnless([]string{"applied", "running", "!stopped", "unrelated"}, toyLogic{}, "applied", []string{"running", "stopped"}, "observer")
	if len(joints) != 2 {
		t.Fatalf("joints = %+v", joints)
	}
	if joints[0].Allowed[0][0] != independence.False || joints[1].Allowed[0][0] != independence.True {
		t.Fatalf("zero truths = %+v", joints)
	}
	if ZeroUnless([]string{"running"}, toyLogic{}, "applied", []string{"running"}, "observer") != nil {
		t.Fatal("constraint without its gate atom")
	}
}

// groupedProducer evaluates `a && b` once on each of two include paths; b
// reaches the guard only on path p2.
type groupedProducer struct{}

func (groupedProducer) Knobs(string, string) []Knob { return toyKnobs[:2] }

func (groupedProducer) Run(_ context.Context, _, _, _ string, knobs []Knob, s Setting) ([]Vector, error) {
	a, b := s.Value(knobs[0]).(bool), s.Value(knobs[1]).(bool)
	t := func(v bool) independence.Truth {
		if v {
			return independence.True
		}
		return independence.False
	}
	return []Vector{
		{Truths: map[string]independence.Truth{"a": t(a), "b": independence.False}, Decision: false, Evaluated: true, Group: "p1"},
		{Truths: map[string]independence.Truth{"a": t(a), "b": t(b)}, Decision: a && b, Evaluated: true, Group: "p2"},
	}, nil
}

// A pair comes from one include path.
func TestGeneratePairsWithinOneGroup(t *testing.T) {
	outcomes, err := Generate(t.Context(), groupedProducer{}, []Target{{
		Site: "g", Recipient: "coordinator", Input: "a",
		Needs:      []map[string]independence.Truth{{"b": independence.True}},
		References: toyRefs,
	}}, Budget{PerTarget: 20, Total: 20})
	contractcheck.FailErr(t, "Generate failed", err)
	if o := outcomes[0]; !o.Found || !strings.Contains(o.With.Vector, "b=T") || !strings.Contains(o.Then.Vector, "b=T") {
		t.Fatalf("outcome = %+v", outcomes[0])
	}
}
