package oarcore

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestConditionAtomsSplitConnectivesAndEvaluateIndependently(t *testing.T) {
	env := sharedTestEnv(t, nil)
	cases := []struct {
		name   string
		source string
		facts  map[string]any
		want   []AtomValue
	}{
		{
			name:   "negation and disjunction are connectives",
			source: `testhost.flag && !(tool == "read" || "a" in testhost.paths)`,
			facts:  map[string]any{"testhost.flag": true, "tool": "read", "testhost.paths": []any{"b"}},
			want: []AtomValue{
				{Atom: "testhost.flag", Value: True},
				{Atom: `tool == "read"`, Value: True},
				{Atom: `"a" in testhost.paths`, Value: False},
			},
		},
		{
			name:   "atoms behind a short-circuit are still read",
			source: `testhost.flag && testhost.num > 2`,
			facts:  map[string]any{"testhost.flag": false, "testhost.num": int64(5)},
			want: []AtomValue{
				{Atom: "testhost.flag", Value: False},
				{Atom: "testhost.num > 2", Value: True},
			},
		},
		{
			name:   "an atom that raises is unknown",
			source: `testhost.flag || 1 / testhost.num == 0`,
			facts:  map[string]any{"testhost.flag": true},
			want: []AtomValue{
				{Atom: "testhost.flag", Value: True},
				{Atom: "(1 / testhost.num) == 0", Value: Unknown},
			},
		},
		{
			name:   "a ternary is one atom",
			source: `(tool == "read" ? testhost.flag : false) && testhost.fact_len("x") == 1`,
			facts:  map[string]any{"tool": "read", "testhost.flag": true, "testhost.fact_len": int64(1)},
			want: []AtomValue{
				{Atom: `(tool == "read") ? testhost.flag : false`, Value: True},
				{Atom: `testhost.fact_len("x") == 1`, Value: True},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := compiledRule(t, tc.source, env).EvaluateAtoms(tc.facts, nil)
			if err != nil {
				t.Fatalf("EvaluateAtoms: %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("atoms = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestRuleAtomsMatchEvaluatedAtoms(t *testing.T) {
	env := sharedTestEnv(t, nil)
	rule := compiledRule(t, `testhost.flag && testhost.flag || tool != "x"`, env)
	if want := []string{"testhost.flag", "testhost.flag", `tool != "x"`}; !slices.Equal(rule.Atoms(), want) {
		t.Fatalf("Atoms() = %v, want %v", rule.Atoms(), want)
	}
	values, err := rule.EvaluateAtoms(map[string]any{"tool": "x"}, nil)
	testutil.FailErr(t, "rule.EvaluateAtoms failed", err)
	if want := []Truth{False, False, False}; !slices.Equal([]Truth{values[0].Value, values[1].Value, values[2].Value}, want) {
		t.Fatalf("values = %+v", values)
	}
	if rule.DecideFromAtoms(map[string]bool{"testhost.flag": false, `tool != "x"`: true}) != true ||
		rule.DecideFromAtoms(map[string]bool{"testhost.flag": true}) != true ||
		rule.DecideFromAtoms(map[string]bool{}) != false {
		t.Fatal("DecideFromAtoms disagrees with the condition's logic")
	}
	if !(&Rule{}).DecideFromAtoms(nil) {
		t.Fatal("a rule without a condition must hold")
	}
	if empty := (&Rule{}).Atoms(); empty != nil {
		t.Fatalf("a rule without a condition has atoms %v", empty)
	}
}

func compiledRule(t *testing.T, source string, env *Environment) *Rule {
	t.Helper()
	root, err := parseCondition(source)
	testutil.FailErr(t, "parseCondition failed", err)
	_, refs, err := checkCondition(root, env)
	testutil.FailErr(t, "checkCondition failed", err)
	return &Rule{When: root, Refs: refs, environment: env}
}
