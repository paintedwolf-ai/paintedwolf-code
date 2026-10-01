package grammars

import (
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

// TestProtoMapFieldKeyTypeKeepsStringTokenChildViaEngine proves that the
// reduce engine restores the anonymous `string` token child of `key_type` on
// a real parse, without help from the (now removed) proto
// normalizeProtoCompatibility post-hoc patch.
// shouldKeepVisibleAnonymousTokenChild keeps different-named
// single-token-wrapper anonymous children unconditionally, so `key_type`
// (named) wrapping `string` (anonymous) is never collapsed to a childless
// leaf in the first place.
func TestProtoMapFieldKeyTypeKeepsStringTokenChildViaEngine(t *testing.T) {
	lang := ProtoLanguage()
	parser := ts.NewParser(lang)
	src := []byte("syntax = \"proto3\";\nmessage M {\n  map<string, int32> m = 1;\n}\n")
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
		t.Fatalf("expected map field to parse cleanly, got %s", root.SExpr(lang))
	}
	keyType := findFirstNamedDescendantWhere(root, lang, "key_type", func(*ts.Node) bool { return true })
	if keyType == nil {
		t.Fatalf("missing key_type node; tree=%s", root.SExpr(lang))
	}
	if got := keyType.ChildCount(); got != 1 {
		t.Fatalf("key_type child count = %d, want 1; tree=%s", got, root.SExpr(lang))
	}
	child := keyType.Child(0)
	if child == nil {
		t.Fatalf("key_type missing string child; node=%s", keyType.SExpr(lang))
	}
	if child.Type(lang) != "string" || child.IsNamed() {
		t.Fatalf("key_type child type/named = %q/%v, want string/false; node=%s", child.Type(lang), child.IsNamed(), keyType.SExpr(lang))
	}
	if child.StartByte() != keyType.StartByte() || child.EndByte() != keyType.EndByte() {
		t.Fatalf("string child byte range = [%d,%d), want [%d,%d) to match parent", child.StartByte(), child.EndByte(), keyType.StartByte(), keyType.EndByte())
	}
}
