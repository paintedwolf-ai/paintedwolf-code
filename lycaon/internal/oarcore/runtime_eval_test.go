package oarcore

// Runtime evaluation of conditions: coercion failures, arithmetic overflow,
// selection and short-circuit behaviour, and fact reading.

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
		{"ternary taken branch", `(tool == "read" ? test.example.num == 3 : false)`, map[string]any{"tool": "read", "test.example.num": int64(3)}, true, "", nil},
		{"ternary other branch", `(tool == "read" ? false : test.example.num == 3)`, map[string]any{"tool": "write", "test.example.num": int64(3)}, true, "", nil},
		{"short circuit and", `false && test.example.fact_len("x") == 4`, map[string]any{}, false, "", nil},
		{"short circuit or", `true || test.example.fact_len("x") == 4`, map[string]any{}, true, "", nil},
		{"in list", `"b" in ["a", "b"]`, map[string]any{}, true, "", nil},
		{"in map", `"k" in test.example.attrs`, map[string]any{"test.example.attrs": map[string]any{"k": "v"}}, true, "", nil},
		{"in scalar raises", `"x" in test.example.attrs["j"]`,
			map[string]any{"test.example.attrs": map[string]any{"j": "abc"}}, false, "in was applied to a scalar", nil},
		{"member comparison", `test.example.attrs.k == "v"`, map[string]any{"test.example.attrs": map[string]any{"k": "v"}}, true, "", nil},
		{"dyn fact compared with number", `test.example.attrs["j"] == 1`,
			map[string]any{"test.example.attrs": map[string]any{"j": "abc"}}, false, "was compared with", nil},
		{"list index", `test.example.paths[0] == "a"`, map[string]any{"test.example.paths": []any{"a", "b"}}, true, "", nil},
		{"list index out of range", `test.example.paths[5] == "a"`,
			map[string]any{"test.example.paths": []any{"a"}}, false, "list index 5 is out of range", nil},
		{"list index negative", `test.example.paths[-1] == "a"`,
			map[string]any{"test.example.paths": []any{"a"}}, false, "out of range", nil},
		{"map index", `test.example.attrs["k"] == "v"`, map[string]any{"test.example.attrs": map[string]any{"k": "v"}}, true, "", nil},
		{"map key absent", `test.example.attrs["nosuch"] == "v"`,
			map[string]any{"test.example.attrs": map[string]any{"k": "v"}}, false, "map key", nil},
		{"index on scalar raises", `test.example.attrs["k"][0] == "v"`,
			map[string]any{"test.example.attrs": map[string]any{"k": "v"}}, false, "applied to a scalar", nil},
		{"addition overflows", `9223372036854775807 + 1 == 0`, map[string]any{}, false, "addition overflows", nil},
		{"subtraction overflows", `-9223372036854775808 - 1 == 0`, map[string]any{}, false, "subtraction overflows", nil},
		{"multiplication overflows", `9223372036854775807 * 2 == 0`, map[string]any{}, false, "multiplication overflows", nil},
		{"division by zero", `1 / 0 == 0`, map[string]any{}, false, "division by zero", nil},
		{"double division by zero", `1.0 / 0.0 > 0.5`, map[string]any{}, false, "division by zero", nil},
		{"negation overflows", `(-(-9223372036854775808)) == 0`, map[string]any{}, false, "negation overflows", nil},
		{"least integer parses", `-9223372036854775808 == -9223372036854775808`, map[string]any{}, true, "", nil},
		{"absent fact reads zero", `test.example.num == 1`, map[string]any{}, false, "", nil},
		{"absent member reads empty", `test.example.attrs.nosuch == ""`,
			map[string]any{"test.example.attrs": map[string]any{}}, true, "", nil},
		{"member not string", `test.example.attrs.k == "v"`,
			map[string]any{"test.example.attrs": map[string]any{"k": int64(3)}}, false, "holds int64 where a string was assumed", nil},
		{"fact wrong type", `test.example.num == 3`,
			map[string]any{"test.example.num": "three"}, false, "is not an int", nil},
		{"function result", `test.example.fact_len("x") == 4`, map[string]any{}, true, "",
			map[string]func(any) (any, error){"test.example.fact_len": func(any) (any, error) { return 4, nil }}},
		{"function result coerced", `test.example.fact_len("x") == 4`, map[string]any{}, false, "is not an int",
			map[string]func(any) (any, error){"test.example.fact_len": func(any) (any, error) { return "four", nil }}},
		{"function error propagates", `test.example.fact_len("x") == 4`, map[string]any{}, false, "observation failed",
			map[string]func(any) (any, error){"test.example.fact_len": func(any) (any, error) { return nil, raise("observation failed") }}},
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
		{`size(test.example.paths) == 2`, map[string]any{"test.example.paths": []any{"a", "b"}}, true},
		{`size(test.example.attrs) == 2`, map[string]any{"test.example.attrs": map[string]any{"a": "1", "b": "2"}}, true},
		{`size(test.example.spans) == 1`, map[string]any{"test.example.spans": []any{map[string]any{"start": 0, "end": 1}}}, true},
		{`size(test.example.attrs.k) == 3`, map[string]any{"test.example.attrs": map[string]any{"k": "abc"}}, true},
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
