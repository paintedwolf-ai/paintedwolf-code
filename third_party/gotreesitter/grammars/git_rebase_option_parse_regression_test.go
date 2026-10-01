package grammars

import (
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

// TestGitRebaseOptionKeepsUppercaseCTokenChildViaEngine proves that the
// reduce engine restores the anonymous `-C` token child of `option` on a
// real parse, without help from a post-hoc AST patch (now removed).
// shouldKeepVisibleAnonymousTokenChild keeps different-named
// single-token-wrapper anonymous children unconditionally, so `option`
// (named) wrapping `-c`/`-C` (anonymous) is never collapsed to a childless
// leaf in the first place.
func TestGitRebaseOptionKeepsUppercaseCTokenChildViaEngine(t *testing.T) {
	lang := GitRebaseLanguage()
	parser := ts.NewParser(lang)
	src := []byte("merge -C H x\n")
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
		t.Fatalf("expected merge -C rebase command to parse cleanly, got %s", root.SExpr(lang))
	}
	option := findFirstNamedDescendantWhere(root, lang, "option", func(*ts.Node) bool { return true })
	if option == nil {
		t.Fatalf("missing option node; tree=%s", root.SExpr(lang))
	}
	if got := option.ChildCount(); got != 1 {
		t.Fatalf("option child count = %d, want 1; tree=%s", got, root.SExpr(lang))
	}
	child := option.Child(0)
	if child == nil {
		t.Fatalf("option missing -C child; node=%s", option.SExpr(lang))
	}
	if child.Type(lang) != "-C" || child.IsNamed() {
		t.Fatalf("option child type/named = %q/%v, want -C/false; node=%s", child.Type(lang), child.IsNamed(), option.SExpr(lang))
	}
	if child.StartByte() != option.StartByte() || child.EndByte() != option.EndByte() {
		t.Fatalf("-C child byte range = [%d,%d), want [%d,%d) to match parent", child.StartByte(), child.EndByte(), option.StartByte(), option.EndByte())
	}
}
