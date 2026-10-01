package grammars

import (
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

// TestGraphQLOperationTypeKeepsQueryTokenChildViaEngine proves that the
// reduce engine restores the anonymous `query` token child of
// `operation_type` on a real parse, without help from a post-hoc AST patch
// (now removed). shouldKeepVisibleAnonymousTokenChild keeps different-named
// single-token-wrapper anonymous children unconditionally, so `operation_type`
// (named) wrapping `query` (anonymous) is never collapsed to a childless
// leaf in the first place.
func TestGraphQLOperationTypeKeepsQueryTokenChildViaEngine(t *testing.T) {
	lang := GraphqlLanguage()
	parser := ts.NewParser(lang)
	src := []byte("query { a }\n")
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("missing root node")
	}
	if tree.ParseStopReason() != ts.ParseStopAccepted {
		t.Fatalf("stop=%s runtime=%s", tree.ParseStopReason(), tree.ParseRuntime().Summary())
	}
	if root.HasError() {
		t.Fatalf("expected minimal query operation to parse cleanly, got %s", root.SExpr(lang))
	}
	opType := findFirstNamedDescendantWhere(root, lang, "operation_type", func(*ts.Node) bool { return true })
	if opType == nil {
		t.Fatalf("missing operation_type node; tree=%s", root.SExpr(lang))
	}
	if got := opType.ChildCount(); got != 1 {
		t.Fatalf("operation_type child count = %d, want 1; tree=%s", got, root.SExpr(lang))
	}
	child := opType.Child(0)
	if child == nil {
		t.Fatalf("operation_type missing query child; node=%s", opType.SExpr(lang))
	}
	if child.Type(lang) != "query" || child.IsNamed() {
		t.Fatalf("operation_type child type/named = %q/%v, want query/false; node=%s", child.Type(lang), child.IsNamed(), opType.SExpr(lang))
	}
	if child.StartByte() != opType.StartByte() || child.EndByte() != opType.EndByte() {
		t.Fatalf("query child byte range = [%d,%d), want [%d,%d) to match parent", child.StartByte(), child.EndByte(), opType.StartByte(), opType.EndByte())
	}
}

// TestGraphQLBooleanValueKeepsTrueTokenChildViaEngine proves that the reduce
// engine restores the anonymous `true` token child of `boolean_value` on a
// real parse, without help from a post-hoc AST patch (now removed).
func TestGraphQLBooleanValueKeepsTrueTokenChildViaEngine(t *testing.T) {
	lang := GraphqlLanguage()
	parser := ts.NewParser(lang)
	src := []byte("query { a(b: true) }\n")
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("missing root node")
	}
	if tree.ParseStopReason() != ts.ParseStopAccepted {
		t.Fatalf("stop=%s runtime=%s", tree.ParseStopReason(), tree.ParseRuntime().Summary())
	}
	if root.HasError() {
		t.Fatalf("expected boolean argument to parse cleanly, got %s", root.SExpr(lang))
	}
	boolValue := findFirstNamedDescendantWhere(root, lang, "boolean_value", func(*ts.Node) bool { return true })
	if boolValue == nil {
		t.Fatalf("missing boolean_value node; tree=%s", root.SExpr(lang))
	}
	if got := boolValue.ChildCount(); got != 1 {
		t.Fatalf("boolean_value child count = %d, want 1; tree=%s", got, root.SExpr(lang))
	}
	child := boolValue.Child(0)
	if child == nil {
		t.Fatalf("boolean_value missing true child; node=%s", boolValue.SExpr(lang))
	}
	if child.Type(lang) != "true" || child.IsNamed() {
		t.Fatalf("boolean_value child type/named = %q/%v, want true/false; node=%s", child.Type(lang), child.IsNamed(), boolValue.SExpr(lang))
	}
	if child.StartByte() != boolValue.StartByte() || child.EndByte() != boolValue.EndByte() {
		t.Fatalf("true child byte range = [%d,%d), want [%d,%d) to match parent", child.StartByte(), child.EndByte(), boolValue.StartByte(), boolValue.EndByte())
	}
}
