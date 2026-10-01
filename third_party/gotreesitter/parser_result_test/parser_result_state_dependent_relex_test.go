package parserresult_test

import (
	"testing"
)

// TestStateDependentRelexKeepsCorrectDerivationAlive is the regression witness
// for the shared-lookahead starvation found while investigating issue #454.
//
// tree-sitter C lexes once per parse version, so two versions in different
// states can receive different tokenizations of the same bytes. This engine
// lexes once for every stack. Where one stack's state requires a different
// symbol for the same bytes, that stack used to find no action, pause, and get
// dropped by the condense step while an unpaused rival survived — and the rival
// then dead-ended, turning the whole file into one ERROR.
//
// Scala is the sharpest witness because `+ - ! ~` are its only prefix
// operators. In `if (a) c + 2` the correct derivation reduces `(a)` to
// _if_condition and needs `+` as the generic operator_identifier, while the
// rival derivation needs the dedicated `+` token that exists so that
// prefix_expression can spell unary plus. The shared lexer emitted the
// dedicated token and starved the correct derivation.
//
// The control cases matter as much as the failures: `*` and `/` are not prefix
// operators, a literal left operand cannot be an infix operator name, and a
// braced body removes the ambiguity entirely. Those parsed correctly before the
// fix and must keep doing so.
func TestStateDependentRelexKeepsCorrectDerivationAlive(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		// Regressions: unbraced control body, bare-identifier left operand,
		// operator that can also be prefix. Every one of these returned a
		// whole-file ERROR before the per-stack re-lex.
		{"if_then_plus", "object O { val x = if (a) c + 2 }"},
		{"if_then_else_plus", "object O { val x = if (a) c + 2 else d }"},
		{"if_then_else_minus", "object O { val x = if (a) c - 2 else d }"},
		{"if_then_bang", "object O { val x = if (a) c ! 2 }"},
		{"if_then_tilde", "object O { val x = if (a) c ~ 2 }"},
		{"if_then_plus_identifier_rhs", "object O { val x = if (a) c + d }"},
		{"if_then_plus_no_space", "object O { val x = if (a) c+2 }"},
		// Not if-specific: any unbraced control body has the same exposure.
		{"while_body_plus", "object O { def f = while (a) c + 2 }"},

		// Controls that already worked and must not regress.
		{"if_then_star", "object O { val x = if (a) c * 2 else d }"},
		{"if_then_slash", "object O { val x = if (a) c / 2 }"},
		{"if_then_literal_left", "object O { val x = if (a) 1 + 2 }"},
		{"if_then_field_left", "object O { val x = if (a) c.f + 2 }"},
		{"braced_body_plus", "object O { val x = if (a) { c + 2 } else d }"},
		{"plain_infix_no_if", "object O { val x = c + 2 }"},
		{"parenthesized_if_then_plus", "object O { val x = (if (a) c) + 2 }"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tree, lang := parseByLanguageName(t, "scala", tc.src)
			root := tree.RootNode()
			if got, want := root.Type(lang), "compilation_unit"; got != want {
				t.Fatalf("root type = %q, want %q: %s", got, want, root.SExpr(lang))
			}
			if root.HasError() {
				t.Fatalf("parse of %q produced an error tree: %s", tc.src, root.SExpr(lang))
			}
			if got, want := root.EndByte(), uint32(len(tc.src)); got != want {
				t.Fatalf("root endByte = %d, want %d (%s)", got, want, root.SExpr(lang))
			}
		})
	}
}

// TestStateDependentRelexProducesOperatorIdentifier pins the shape, not just
// the absence of an error. The C oracle lexes `+` here as the grammar's generic
// operator_identifier — the same token it uses for `*` — and the Go tree must
// agree, otherwise we could satisfy the error check above with some other
// derivation that merely happens to avoid ERROR.
func TestStateDependentRelexProducesOperatorIdentifier(t *testing.T) {
	plusTree, lang := parseByLanguageName(t, "scala", "object O { val x = if (a) c + 2 }")
	starTree, _ := parseByLanguageName(t, "scala", "object O { val x = if (a) c * 2 }")

	plus := plusTree.RootNode().SExpr(lang)
	star := starTree.RootNode().SExpr(lang)
	if plus != star {
		t.Fatalf("additive and multiplicative bodies must share one shape.\n  plus: %s\n  star: %s", plus, star)
	}
	for _, want := range []string{"if_expression", "infix_expression", "operator_identifier"} {
		if !containsSubstring(plus, want) {
			t.Fatalf("expected %q in parse shape: %s", want, plus)
		}
	}
	if containsSubstring(plus, "prefix_expression") {
		t.Fatalf("`+` was lexed as a prefix operator rather than an infix operator_identifier: %s", plus)
	}
}

func containsSubstring(haystack, needle string) bool {
	return len(needle) == 0 || len(haystack) >= len(needle) && indexOfSubstring(haystack, needle) >= 0
}

func indexOfSubstring(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
