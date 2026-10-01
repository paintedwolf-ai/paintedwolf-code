package grammars

import (
	"strings"
	"testing"

	"github.com/odvcencio/gotreesitter"
)

// TestSwiftSimpleIdentifierAliasedAsTypeIdentifier pins the runtime behavior
// backing one row of swift.bin's NonTerminalAliasMap: tree-sitter-swift's
// _simple_user_type rule aliases $.simple_identifier to $.type_identifier
// (grammar.js: `alias($.simple_identifier, $.type_identifier)`), so an
// identifier used in type-name position must display as type_identifier,
// while the SAME simple_identifier production reached unaliased (an
// ordinary identifier expression) must display as plain simple_identifier.
//
// This is the "real" behavioral half of TestSwiftNonTerminalAliasMapParity in
// grammargen: that test only checks the derived NonTerminalAliasMap table is
// structurally identical between grammargen and ts2go; this test checks the
// shipped grammars.SwiftLanguage() blob actually PRODUCES the two distinct
// shapes on live input.
func TestSwiftSimpleIdentifierAliasedAsTypeIdentifier(t *testing.T) {
	lang := SwiftLanguage()
	src := []byte("let x: Int = 1\nlet y = x\n")
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("produced parse errors: %s", root.SExpr(lang))
	}
	sexpr := root.SExpr(lang)
	if !strings.Contains(sexpr, "(type_identifier") {
		t.Fatalf("expected type_identifier (aliased simple_identifier) for the `Int` type annotation: %s", sexpr)
	}
	if !strings.Contains(sexpr, "(simple_identifier") {
		t.Fatalf("expected an unaliased simple_identifier for the `x` reference expression: %s", sexpr)
	}
}

// TestSwiftValueArgumentAliasedAsInterpolatedExpression pins the runtime
// behavior backing another row of swift.bin's NonTerminalAliasMap:
// tree-sitter-swift's _interpolation_contents rule aliases $.value_argument
// to $.interpolated_expression (grammar.js: `alias($.value_argument,
// $.interpolated_expression)`), so a value inside a string interpolation
// (`"\(bar)"`) must display as interpolated_expression, while the SAME
// value_argument production reached unaliased (an ordinary call argument)
// must display as plain value_argument.
func TestSwiftValueArgumentAliasedAsInterpolatedExpression(t *testing.T) {
	lang := SwiftLanguage()
	src := []byte("let bar = 1\nfoo(bar)\nlet s = \"\\(bar)\"\n")
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("produced parse errors: %s", root.SExpr(lang))
	}
	sexpr := root.SExpr(lang)
	if !strings.Contains(sexpr, "(interpolated_expression") {
		t.Fatalf("expected interpolated_expression (aliased value_argument) inside the string literal: %s", sexpr)
	}
	if !strings.Contains(sexpr, "(value_argument") {
		t.Fatalf("expected an unaliased value_argument for the `foo(bar)` call: %s", sexpr)
	}
}
