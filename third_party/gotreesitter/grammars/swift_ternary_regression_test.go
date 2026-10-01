package grammars

import (
	"strings"
	"testing"

	"github.com/odvcencio/gotreesitter"
)

// issue #135: the Swift ternary/conditional operator `? :` never parsed as
// ternary_expression — every use produced an ERROR node, and inside function
// bodies the surrounding function_declaration degraded to
// _modifierless_function_declaration_no_body, losing structure entirely.
//
// Root cause: grammars/grammar_blobs/swift.bin was a stale grammargen blob
// (last regenerated 2026-05-04, ~80 grammargen normalize/lr/dfa commits
// behind at fix time), including a hidden-string-token collision fix
// (2026-06-17) and an LR shift-reduce priority fix for immediate shifts
// (2026-06-29). On the stale blob, "?" was represented by two anonymous
// terminals that were both incorrectly marked as token.immediate()
// (ImmediateTokens), which starved the ternary_expression production of its
// "?" shift action and routed every ternary through the optional-chaining/
// postfix-"?" production instead (visible as `directly_assignable_expression`
// wrapping in the ERROR tree).
//
// The fix has two parts:
//  1. Regenerating swift.bin from the current grammargen/swift_grammar.go,
//     which fixes the symbol/immediate-token classification and restores
//     ternary_expression.
//  2. A generator-side DFA fix (grammargen/dfa.go, grammargen/diagnostics.go)
//     for an unbounded missing-token-recovery lex-mode widening that was
//     blowing up compiled DFA state count on regeneration; without narrowing
//     that widening through a follow-token allowlist, swift.bin would
//     regenerate correctly but multiple times larger and far slower to
//     build. See buildLexModeMissingRecoveryTokensFunc in
//     grammargen/diagnostics.go for the narrowing (and its known, accepted
//     recovery gap — see TestSwiftIncompleteExpressionKnownGap below).
func TestSwiftTernaryExpressionParses(t *testing.T) {
	lang := SwiftLanguage()
	cases := []struct {
		name string
		src  string
	}{
		{"top-level-let", "let y = 3 > 0 ? 1 : 2"},
		{"return-in-function", "func f() -> Int { return true ? 1 : 2 }"},
		{"call-argument", "foo(true ? 1 : 2)"},
		{"nested-ternary", "let a = condition ? doSomething() : doSomethingElse()"},
		{"optional-unwrap", "let opt: Int? = 5\nlet val = opt != nil ? opt! : 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(tc.src)
			parser := gotreesitter.NewParser(lang)
			tree, err := parser.Parse(src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			defer tree.Release()
			root := tree.RootNode()
			if root.HasError() {
				t.Fatalf("ternary expression produced parse errors: %s", root.SExpr(lang))
			}
			sexpr := root.SExpr(lang)
			if !strings.Contains(sexpr, "(ternary_expression") {
				t.Fatalf("missing ternary_expression in tree: %s", sexpr)
			}
		})
	}
}

// TestSwiftTernaryFreeControlFlowUnaffected guards against a regression that
// only affects ternary-bearing sources; plain if/else control flow (which
// already passed before the fix) must remain error-free.
func TestSwiftTernaryFreeControlFlowUnaffected(t *testing.T) {
	lang := SwiftLanguage()
	src := []byte("if true { print(1) } else { print(2) }")
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("ternary-free control flow reported error: %s", root.SExpr(lang))
	}
}

// TestSwiftIncompleteExpressionKnownGap PINS a known, accepted parse-error
// recovery gap introduced as a side effect of the missing-token-recovery
// lex-mode narrowing in grammargen/diagnostics.go's
// buildLexModeMissingRecoveryTokensFunc (needed to keep swift.bin's compiled
// DFA state count sane on regeneration — see the package doc comment above).
//
// LOUD NOTE: `let x = ` is an incomplete expression — the reference C
// tree-sitter oracle for Swift flags this as a parse ERROR (missing the
// right-hand-side expression). Our generated grammar currently does NOT,
// because missing-token recovery's lookahead widening no longer admits
// expression-start (identifier/literal) tokens into this state's lex mode,
// only closing/terminator punctuation and keywords (see the KNOWN GAP note
// on buildLexModeMissingRecoveryTokensFunc). This test asserts the CURRENT,
// accepted behavior (HasError() == false) specifically so that behavior
// cannot drift silently in either direction:
//
//   - If a future change narrows recovery further and this starts producing
//     an ERROR tree for a DIFFERENT reason, investigate before "fixing" this
//     test.
//   - If a future recovery-widening experiment (see the diagnostics.go
//     follow-up note) closes this gap and `let x = ` starts correctly
//     reporting HasError() == true, UPDATE this test to assert true and
//     delete this gap note — do not just flip the assertion blindly without
//     removing the now-stale commentary.
func TestSwiftIncompleteExpressionKnownGap(t *testing.T) {
	lang := SwiftLanguage()
	src := []byte("let x = ")
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("known gap regression: `let x = ` now reports a parse error (%s) — "+
			"if this is from the intended recovery-widening fix, update this test to "+
			"assert HasError()==true and remove the known-gap commentary; otherwise "+
			"investigate the unrelated cause", root.SExpr(lang))
	}
}
