package oarcore

// Selector matching against occurrences, and fact reading.

import (
	"strings"
	"testing"
)

func TestMatchSelectorTools(t *testing.T) {
	env := sharedTestEnv(t, nil)
	rule, _, err := LoadDocument(ruleDoc(func(d map[string]any) {
		d["selector"] = map[string]any{"tools": []any{"read", "write"}}
	}), env)
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	cases := []struct {
		name  string
		facts map[string]any
		want  bool
	}{
		{"listed tool", map[string]any{"tool": "read"}, true},
		{"unlisted tool", map[string]any{"tool": "grep"}, false},
		{"no tool invoked", map[string]any{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rule.MatchSelector(tc.facts)
			if err != nil {
				t.Fatalf("MatchSelector: %v", err)
			}
			if got != tc.want {
				t.Fatalf("MatchSelector = %v, want %v", got, tc.want)
			}
		})
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
	v, err = rule.Fact("testhost.num", map[string]any{})
	if err != nil || v != int64(0) {
		t.Fatalf("absent fact: %v %v", v, err)
	}
	if _, err = rule.Fact("testhost.num", map[string]any{"testhost.num": "three"}); err == nil ||
		!strings.Contains(err.Error(), "is not an int") {
		t.Fatalf("wrong type: %v", err)
	}
	v, err = rule.Fact("testhost.num", map[string]any{"testhost.num": 3})
	if err != nil || v != int64(3) {
		t.Fatalf("int fact: %v %v", v, err)
	}
	v, err = rule.Fact("testhost.paths", map[string]any{"testhost.paths": []string{"a"}})
	if err != nil {
		t.Fatalf("typed list fact: %v", err)
	}
	if list, ok := v.([]string); !ok || len(list) != 1 {
		t.Fatalf("typed list value: %#v", v)
	}
}
