package oarcore

// Table tests for the condition-language parser and the type checker: every
// rejection names the offending construct and its offset.

import (
	"strings"
	"testing"
)

func rejectTestEnv(t *testing.T) *Environment {
	return sharedTestEnv(t, nil)
}

func TestCheckConditionRejectsForeignConstructs(t *testing.T) {
	env := rejectTestEnv(t)
	cases := []struct {
		name   string
		source string
		want   string
	}{
		{"line comment", `tool == "read" // note`, "OAR-EXPR-6"},
		{"block comment", `/* note */ tool == "read"`, "OAR-EXPR-6"},
		{"hash comment", `tool == "read" # note`, "OAR-EXPR-6"},
		{"map literal", `{} == 1`, "OAR-EXPR-1"},
		{"leading dot", `.tool == "read"`, "OAR-EXPR-1"},
		{"dangling dot", `testhost.attrs. == "v"`, "OAR-EXPR-1"},
		{"assignment", `tool = "read"`, "OAR-EXPR-1"},
		{"bitwise and", `tool & tool`, "OAR-EXPR-1"},
		{"unexpected character", `tool @ tool`, "OAR-EXPR-1"},
		{"unterminated string", `"abc == 1`, "OAR-EXPR-1"},
		{"literal newline in string", "\"a\nb\" == 1", "OAR-EXPR-1"},
		{"unsupported escape", `"\q" == 1`, "OAR-EXPR-3"},
		{"unicode escape", `"\u0041" == "A"`, "OAR-EXPR-3"},
		{"exponent without digits", `1e == 1.0`, "OAR-EXPR-1"},
		{"two points", `1.5.5 == 1`, "OAR-EXPR-1"},
		{"trailing point", `1. == 1`, "OAR-EXPR-1"},
		{"unsigned suffix", `1u == 1`, "OAR-EXPR-1"},
		{"number with letter", `1x == 1`, "OAR-EXPR-1"},
		{"int overflow", `9223372036854775808 == 0`, "OAR-EXPR-15"},
		{"double overflow", strings.Repeat("9", 310) + ".0 == 1.0", "OAR-EXPR-15"},
		{"exponent overflow", `1e400 > 0.0`, "OAR-EXPR-15"},
		{"index", `tool_args["path"] == "x"`, "OAR-EXPR-1"},
		{"two relops", `tool == "a" == "b"`, "OAR-EXPR-4"},
		{"trailing operator", `size(`, "OAR-EXPR-1"},
		{"reserved in", `in == true`, "OAR-EXPR-6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckCondition(tc.source, env)
			if err == nil {
				t.Fatalf("CheckCondition(%q) accepted", tc.source)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CheckCondition(%q) = %q, want substring %q", tc.source, err.Error(), tc.want)
			}
		})
	}
}

func TestCheckConditionRejectsTypeErrors(t *testing.T) {
	env := rejectTestEnv(t)
	cases := []struct {
		name   string
		source string
		want   string
	}{
		{"empty list", `tool in []`, "OAR-EXPR-7"},
		{"mixed list", `tool in ["a", 1]`, "OAR-EXPR-7"},
		{"unknown identifier", `nosuchfact == 1`, "OAR-EXPR-2"},
		{"selection from non-map", `testhost.num.x == "1"`, "OAR-EXPR-2"},
		{"function as identifier", `testhost.fact_len == 1`, "OAR-EXPR-16"},
		{"size arity zero", `size() == 0`, "OAR-EXPR-16"},
		{"size arity two", `size("a", "b") == 0`, "OAR-EXPR-16"},
		{"size scalar", `size(1) == 0`, "OAR-EXPR-16"},
		{"string builtin arity", `starts_with("a")`, "OAR-EXPR-16"},
		{"string builtin types", `starts_with(1, "a")`, "OAR-EXPR-16"},
		{"fact called", `testhost.num("x") == 1`, "OAR-EXPR-16"},
		{"unknown call", `nosuchfn("x") == 1`, "OAR-EXPR-16"},
		{"function arity zero", `testhost.fact_len() == 0`, "OAR-EXPR-16"},
		{"function arity two", `testhost.fact_len("a", "b") == 0`, "OAR-EXPR-16"},
		{"function argument type", `testhost.fact_len(1) == 0`, "OAR-EXPR-16"},
		{"counter reader without literal", `fire_count_of(tool) == 0`, "OAR-FIRE-6"},
		{"bang on non-bool", `!testhost.num`, "OAR-EXPR-8"},
		{"minus on string", `-testhost.str == 1`, "OAR-EXPR-9"},
		{"ternary condition", `(1 ? 1 : 2) == 1`, "OAR-EXPR-8"},
		{"ternary branches differ", `(true ? 1 : "x") == 1`, "OAR-EXPR-8"},
		{"and non-bool", `testhost.num && true`, "OAR-EXPR-8"},
		{"in left scalar", `1 in ["a"]`, "OAR-EXPR-12"},
		{"in right scalar", `"a" in 1`, "OAR-EXPR-12"},
		{"aggregate equality", `testhost.paths == testhost.paths`, "OAR-EXPR-11"},
		{"unrelated equality", `"a" == 1`, "OAR-EXPR-11"},
		{"int against double", `testhost.num == 1.0`, "OAR-EXPR-11"},
		{"ordering mixed", `testhost.str < 1`, "OAR-EXPR-10"},
		{"ordering int against double", `testhost.ratio > 1`, "OAR-EXPR-10"},
		{"modulo on double", `testhost.ratio % 2.0 == 0.0`, "OAR-EXPR-9"},
		{"addition mixed", `testhost.num + 1.0 == 2.0`, "OAR-EXPR-9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckCondition(tc.source, env)
			if err == nil {
				t.Fatalf("CheckCondition(%q) accepted", tc.source)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CheckCondition(%q) = %q, want substring %q", tc.source, err.Error(), tc.want)
			}
		})
	}
}

func TestCheckConditionAcceptsTypedConditions(t *testing.T) {
	env := rejectTestEnv(t)
	for _, source := range []string{
		`tool == "read" && !testhost.flag`,
		`testhost.num + 1 > 2 % 3`,
		`"a" + "b" == "ab"`,
		`size(testhost.paths) == 2 && size(testhost.attrs) > 0`,
		`starts_with(tool, "t") && "a" in testhost.paths`,
		`testhost.attrs.k == "v" ? testhost.ratio >= 0.5 : false`,
		`"k" in testhost.attrs && testhost.attrs.missing == ""`,
		`-9223372036854775808 < 0 && 1.5e3 > 1e2`,
		`-testhost.num <= 0`,
		`(true ? 1 : 2) == 1`,
		`testhost.fact_len("x") == 4`,
	} {
		if err := CheckCondition(source, env); err != nil {
			t.Fatalf("CheckCondition(%q) = %v", source, err)
		}
	}
	if err := CheckCondition("", env); err != nil {
		t.Fatalf("CheckCondition(\"\") = %v", err)
	}
	if err := CheckCondition("", nil); err != nil {
		t.Fatalf("CheckCondition(\"\") with nil env = %v", err)
	}
}

func TestCheckConditionNodeLimit(t *testing.T) {
	env := sharedTestEnv(t, func(spec *EnvironmentSpec) { spec.ExpressionNodesMax = 2 })
	err := CheckCondition(`true && true && true`, env)
	if err == nil || !strings.Contains(err.Error(), "parse-tree nodes") {
		t.Fatalf("node limit: %v", err)
	}
}
