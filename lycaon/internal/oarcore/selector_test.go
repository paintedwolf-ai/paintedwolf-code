package oarcore

// Selector matching against occurrences, fact reading, and transform target
// validation.

import (
	"strings"
	"testing"
)

func TestMatchSelectorClauses(t *testing.T) {
	env := sharedTestEnv(t, nil)
	load := func(t *testing.T, selector map[string]any) *Rule {
		t.Helper()
		rule, _, err := LoadDocument(ruleDoc(func(d map[string]any) { d["selector"] = selector }), env)
		if err != nil {
			t.Fatalf("LoadDocument: %v", err)
		}
		return rule
	}

	cases := []struct {
		name  string
		sel   map[string]any
		facts map[string]any
		dims  Dimensions
		want  bool
	}{
		{"tool matches", map[string]any{"tool": "read"}, map[string]any{"tool": "read"}, nil, true},
		{"tool differs", map[string]any{"tool": "write"}, map[string]any{"tool": "read"}, nil, false},
		{"tools list matches", map[string]any{"tools": []any{"read", "write"}}, map[string]any{"tool": "read"}, nil, true},
		{"core fact absent", map[string]any{"tool": "read"}, map[string]any{}, nil, false},
		{"unmodeled dimension", map[string]any{"phase": "run"}, map[string]any{}, nil, false},
		{"modeled dimension", map[string]any{"phase": "run"}, map[string]any{}, Dimensions{"phase": "run"}, true},
		{"modeled dimension differs", map[string]any{"phase": "run"}, map[string]any{}, Dimensions{"phase": "stop"}, false},
		{"null clause on unbound", map[string]any{"surface": nil}, map[string]any{}, Dimensions{"surface": nil}, true},
		{"null clause on bound", map[string]any{"surface": nil}, map[string]any{}, Dimensions{"surface": "worker"}, false},
		{"ledger member matches", map[string]any{"ledger": map[string]any{"test.example.paths": []any{"a"}}},
			map[string]any{"test.example.paths": []any{"a", "b"}}, nil, true},
		{"ledger member differs", map[string]any{"ledger": map[string]any{"test.example.paths": []any{"z"}}},
			map[string]any{"test.example.paths": []any{"a", "b"}}, nil, false},
		{"ledger absent fact", map[string]any{"ledger": map[string]any{"test.example.paths": []any{"a"}}},
			map[string]any{}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule := load(t, tc.sel)
			got, err := rule.MatchSelector(tc.facts, tc.dims)
			if err != nil {
				t.Fatalf("MatchSelector: %v", err)
			}
			if got != tc.want {
				t.Fatalf("MatchSelector = %v, want %v", got, tc.want)
			}
		})
	}

	// A ledger error surfaces as an evaluation failure, not a non-match.
	rule := load(t, map[string]any{"ledger": map[string]any{"test.example.paths": []any{"a"}}})
	_, err := rule.MatchSelector(map[string]any{"test.example.paths": []any{1}}, nil)
	if err == nil || !strings.Contains(err.Error(), "is not a list<string>") {
		t.Fatalf("ledger coercion: %v", err)
	}

	// A ledger clause over an undeclared fact never matches.
	rule = load(t, map[string]any{"tool": "read"})
	clause := SelectorClause{Dimension: "ledger", Ledger: map[string][]string{"test.example.nofact": {"x"}}}
	ok, err := rule.MatchSelector(map[string]any{"tool": "read"}, nil)
	if err != nil || !ok {
		t.Fatalf("baseline match: %v %v", ok, err)
	}
	rule.Selector = append(rule.Selector, clause)
	ok, err = rule.MatchSelector(map[string]any{"tool": "read"}, nil)
	if err != nil || ok {
		t.Fatalf("undeclared ledger fact matched: %v %v", ok, err)
	}

	// The principal clause accepts caller ids as a list.
	rule = load(t, map[string]any{"principal": []any{"user:1", "user:2"}})
	ok, err = rule.MatchSelector(map[string]any{"tool": "read"}, Dimensions{"principal": "user:1"})
	if err != nil || !ok {
		t.Fatalf("principal list: %v %v", ok, err)
	}
}

func TestRuleFactReading(t *testing.T) {
	env := sharedTestEnv(t, nil)
	rule, _, err := LoadDocument(ruleDoc(nil), env)
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	v, err := rule.Fact("nosuch", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "undeclared fact") {
		t.Fatalf("undeclared fact: %v %v", v, err)
	}
	v, err = rule.Fact("test.example.num", map[string]any{})
	if err != nil || v != int64(0) {
		t.Fatalf("absent fact: %v %v", v, err)
	}
	if _, err = rule.Fact("test.example.num", map[string]any{"test.example.num": "three"}); err == nil ||
		!strings.Contains(err.Error(), "is not an int") {
		t.Fatalf("wrong type: %v", err)
	}
	v, err = rule.Fact("test.example.num", map[string]any{"test.example.num": 3})
	if err != nil || v != int64(3) {
		t.Fatalf("int fact: %v %v", v, err)
	}
	v, err = rule.Fact("test.example.paths", map[string]any{"test.example.paths": []string{"a"}})
	if err != nil {
		t.Fatalf("typed list fact: %v", err)
	}
	if list, ok := v.([]string); !ok || len(list) != 1 {
		t.Fatalf("typed list value: %#v", v)
	}
}

func TestValidateTransformTarget(t *testing.T) {
	if err := ValidateTransformTarget("content", nil, 0); err != nil {
		t.Fatalf("content target: %v", err)
	}
	err := ValidateTransformTarget("test.example.spans", map[string]any{"test.example.spans": "notalist"}, 5)
	if err == nil || !strings.Contains(err.Error(), "transform target must be list<map>") {
		t.Fatalf("scalar target: %v", err)
	}
	err = ValidateTransformTarget("test.example.spans", map[string]any{"test.example.spans": []any{"x"}}, 5)
	if err == nil || !strings.Contains(err.Error(), "not an object") {
		t.Fatalf("bad span: %v", err)
	}
	spans := map[string]any{"test.example.spans": []any{
		map[string]any{"start": int64(0), "end": int64(3)},
	}}
	if err := ValidateTransformTarget("test.example.spans", spans, 5); err != nil {
		t.Fatalf("valid spans: %v", err)
	}
}
