package oarcore

// Runtime evaluation of conditions: coercion failures, arithmetic faults,
// member selection and short-circuit behaviour, and fact reading.

import (
	"strings"
	"testing"
)

func TestEvaluateConditionRuntime(t *testing.T) {
	env := sharedTestEnv(t, nil)
	ok, err := EvaluateCondition("", env, map[string]any{}, nil)
	if err != nil || !ok {
		t.Fatalf("empty condition: %v %v", ok, err)
	}
	cases := []struct {
		name     string
		source   string
		facts    map[string]any
		want     bool
		wantErr  string
		function map[string]func(any) (any, error)
	}{
		{"identity", `tool == "read"`, map[string]any{"tool": "read"}, true, "", nil},
		{"ternary taken branch", `(tool == "read" ? testhost.num == 3 : false)`, map[string]any{"tool": "read", "testhost.num": int64(3)}, true, "", nil},
		{"ternary other branch", `(tool == "read" ? false : testhost.num == 3)`, map[string]any{"tool": "write", "testhost.num": int64(3)}, true, "", nil},
		{"short circuit and", `false && testhost.fact_len("x") == 4`, map[string]any{}, false, "", nil},
		{"short circuit or", `true || testhost.fact_len("x") == 4`, map[string]any{}, true, "", nil},
		{"in list", `"b" in ["a", "b"]`, map[string]any{}, true, "", nil},
		{"in map", `"k" in testhost.attrs`, map[string]any{"testhost.attrs": map[string]any{"k": "v"}}, true, "", nil},
		{"member comparison", `testhost.attrs.k == "v"`, map[string]any{"testhost.attrs": map[string]any{"k": "v"}}, true, "", nil},
		{"addition overflows", `9223372036854775807 + 1 == 0`, map[string]any{}, false, "addition overflows", nil},
		{"subtraction overflows", `-9223372036854775808 - 1 == 0`, map[string]any{}, false, "subtraction overflows", nil},
		{"multiplication overflows", `9223372036854775807 * 2 == 0`, map[string]any{}, false, "multiplication overflows", nil},
		{"division by zero", `1 / 0 == 0`, map[string]any{}, false, "division by zero", nil},
		{"double division by zero", `1.0 / 0.0 > 0.5`, map[string]any{}, false, "division by zero", nil},
		{"negation overflows", `(-(-9223372036854775808)) == 0`, map[string]any{}, false, "negation overflows", nil},
		{"least integer parses", `-9223372036854775808 == -9223372036854775808`, map[string]any{}, true, "", nil},
		{"absent fact reads zero", `testhost.num == 1`, map[string]any{}, false, "", nil},
		{"absent member reads empty", `testhost.attrs.nosuch == ""`,
			map[string]any{"testhost.attrs": map[string]any{}}, true, "", nil},
		{"member not string", `testhost.attrs.k == "v"`,
			map[string]any{"testhost.attrs": map[string]any{"k": int64(3)}}, false, "OAR-EXPR-13", nil},
		{"fact wrong type", `testhost.num == 3`,
			map[string]any{"testhost.num": "three"}, false, "is not an int", nil},
		{"function result", `testhost.fact_len("x") == 4`, map[string]any{}, true, "",
			map[string]func(any) (any, error){"testhost.fact_len": func(any) (any, error) { return 4, nil }}},
		{"function result coerced", `testhost.fact_len("x") == 4`, map[string]any{}, false, "is not an int",
			map[string]func(any) (any, error){"testhost.fact_len": func(any) (any, error) { return "four", nil }}},
		{"function error propagates", `testhost.fact_len("x") == 4`, map[string]any{}, false, "observation failed",
			map[string]func(any) (any, error){"testhost.fact_len": func(any) (any, error) { return nil, raise("observation failed") }}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := EvaluateCondition(tc.source, env, tc.facts, tc.function)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("EvaluateCondition(%q) accepted", tc.source)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("EvaluateCondition(%q) = %q, want substring %q", tc.source, err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("EvaluateCondition(%q) = %v", tc.source, err)
			}
			if ok != tc.want {
				t.Fatalf("EvaluateCondition(%q) = %v, want %v", tc.source, ok, tc.want)
			}
		})
	}
}

func TestSizeOfKinds(t *testing.T) {
	env := sharedTestEnv(t, nil)
	cases := []struct {
		source string
		facts  map[string]any
		want   bool
	}{
		{`size("abc") == 3`, map[string]any{}, true},
		{`size(testhost.paths) == 2`, map[string]any{"testhost.paths": []any{"a", "b"}}, true},
		{`size(testhost.attrs) == 2`, map[string]any{"testhost.attrs": map[string]any{"a": "1", "b": "2"}}, true},
		{`size(testhost.spans) == 1`, map[string]any{"testhost.spans": []any{map[string]any{"start": 0, "end": 1}}}, true},
		{`size(testhost.attrs.k) == 3`, map[string]any{"testhost.attrs": map[string]any{"k": "abc"}}, true},
	}
	for _, tc := range cases {
		ok, err := EvaluateCondition(tc.source, env, tc.facts, nil)
		if tc.want {
			if err != nil || !ok {
				t.Fatalf("EvaluateCondition(%q) = %v, %v", tc.source, ok, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "size() was applied to a scalar") {
			t.Fatalf("EvaluateCondition(%q) = %v, %v", tc.source, ok, err)
		}
	}
}
