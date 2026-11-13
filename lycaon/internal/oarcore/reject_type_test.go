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
		{"line comment", `tool == "read" // note`, "a comment is not part of the condition language"},
		{"block comment", `/* note */ tool == "read"`, "a comment is not part of the condition language"},
		{"hash comment", `tool == "read" # note`, "a comment is not part of the condition language"},
		{"map literal", `{} == 1`, "a map literal is not part of the condition language"},
		{"leading dot", `.tool == "read"`, "there is no field-selection operator"},
		{"dangling dot", `test.example.attrs. == "v"`, `a "." must be followed by a name`},
		{"assignment", `tool = "read"`, "assignment is not part of the condition language"},
		{"bitwise and", `tool & tool`, "bitwise"},
		{"unexpected character", `tool @ tool`, "unexpected character"},
		{"unterminated string", `"abc == 1`, "unterminated string literal"},
		{"literal newline in string", "\"a\nb\" == 1", "literal newline in string"},
		{"unsupported escape", `"\q" == 1`, "unsupported string escape"},
		{"short unicode escape", `"\u12" == 1`, "must be followed by exactly four hexadecimal digits"},
		{"bad hex digits", `"\u00zz" == 1`, "must be followed by exactly four hexadecimal digits"},
		{"exponent after point", `1.5e3 == 1`, "exponent notation is not part of the condition language"},
		{"exponent on int", `15e3 == 1`, "exponent notation is not part of the condition language"},
		{"two points", `1.5.5 == 1`, "malformed number literal"},
		{"trailing point", `1. == 1`, "malformed number literal"},
		{"unsigned suffix", `1u == 1`, "an unsigned literal is not part of the condition language"},
		{"number with letter", `1x == 1`, "malformed number literal"},
		{"int overflow", `9223372036854775808 == 0`, "does not fit int"},
		{"double overflow", strings.Repeat("9", 310) + ".0 == 1", "no finite binary64 value"},
		{"repeated bang", `!!true == true`, "a unary operator may not be repeated"},
		{"repeated minus", `- - 1 == 0`, "a unary operator may not be repeated"},
		{"two relops", `tool == "a" == "b"`, "a relation admits at most one relational operator"},
		{"trailing operator", `size(`, "expected an expression"},
		{"reserved in", `in == true`, `"in" is a reserved word`},
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
		{"empty list", `[] == 1`, "no inferable type"},
		{"mixed list", `["a", 1] == 1`, "a list literal is list<string> or list<map>"},
		{"unknown identifier", `nosuchfact == 1`, "unknown identifier"},
		{"dotted unknown with hint", `test.example.num.bogus == 1`, "only a map<string, dyn> fact has members to select"},
		{"function as identifier", `test.example.fact_len == 1`, "is an observation function and must be called"},
		{"size arity zero", `size() == 0`, "size() takes exactly one argument"},
		{"size arity two", `size("a", "b") == 0`, "size() takes exactly one argument"},
		{"size scalar", `size(1) == 0`, "size() takes a string, a list, or a map"},
		{"string builtin arity", `starts_with("a") == true`, "takes exactly two arguments"},
		{"string builtin types", `starts_with(1, "a") == true`, "takes two string arguments"},
		{"fact called", `test.example.num("x") == 1`, "is a fact, not an observation function"},
		{"unknown call", `nosuchfn("x") == 1`, "unknown identifier"},
		{"function arity zero", `test.example.fact_len() == 0`, "takes exactly one argument, given 0"},
		{"function arity two", `test.example.fact_len("a", "b") == 0`, "takes exactly one argument, given 2"},
		{"function argument type", `test.example.fact_len(1) == 0`, "takes string, given int"},
		{"index on scalar", `test.example.num["k"] == 1`, "the index operator does not accept int indexed by string"},
		{"index list by string", `test.example.paths["k"] == "a"`, "the index operator does not accept list<string> indexed by string"},
		{"bang on non-bool", `!test.example.num == true`, "! takes bool, given int"},
		{"minus on string", `-test.example.str == 1`, "unary - takes int or double, given string"},
		{"minus on selected member", `-test.example.attrs.k == 1`, "unary - takes int or double, given string"},
		{"ternary condition", `(1 ? 1 : 2) == 1`, "the ternary condition must be bool"},
		{"ternary branches differ", `(true ? 1 : "x") == 1`, "the ternary branches are int and string"},
		{"and non-bool", `test.example.num && true == true`, "&& takes bool operands"},
		{"in left scalar", `1 in ["a"]`, "in takes a string on the left"},
		{"in right scalar", `"a" in 1`, "in takes a list<string> or a map on the right"},
		{"aggregate equality", `["a"] == ["b"]`, "does not compare two aggregate values"},
		{"unrelated equality", `"a" == 1`, "compares unrelated types string and int"},
		{"ordering mixed", `test.example.str < 1`, "compares string with int"},
		{"add on unrelated hints", `test.example.attrs.a + test.example.attrs.b == 1`, "compares unrelated types"},
		{"modulo on string", `test.example.str % 2 == 0`, "takes two int operands"},
		{"addition mixed", `test.example.str + 1 == "x"`, "takes two int or two double operands"},
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
		`tool == "read" && !test.example.flag`,
		`test.example.num + 1 > 2 % 3`,
		`"a" + "b" == "ab"`,
		`size(test.example.paths) == 2 && size(test.example.attrs) > 0`,
		`starts_with(tool, "t") && "a" in test.example.paths`,
		`test.example.attrs.k == "v" ? test.example.ratio >= 0.5 : false`,
		`test.example.paths[0] == "a" && test.example.attrs["k"] == "v"`,
		`-test.example.num <= 0`,
		`(true ? 1 : 2) == 1`,
		`test.example.fact_len("x") == 4`,
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
	if err == nil || !strings.Contains(err.Error(), "expression_nodes_max") {
		t.Fatalf("node limit: %v", err)
	}
}
