//go:build !grammar_subset || grammar_subset_gomod

package grammars

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func TestGomodGodebugRecoveryPreservesFollowingRequireDirective(t *testing.T) {
	src := []byte("module example.com/m\n\ngo 1.26.0\n\ngodebug default=go1.26\n\nrequire (\n\texample.com/a v1.2.3\n)\n")
	lang := GomodLanguage()
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	defer tree.Release()

	root := tree.RootNode()
	if root == nil {
		t.Fatal("parse returned nil root")
	}
	if tree.ParseStopReason() != gotreesitter.ParseStopAccepted {
		t.Fatalf("stop=%s runtime=%s", tree.ParseStopReason(), tree.ParseRuntime().Summary())
	}
	if rt := tree.ParseRuntime(); rt.Truncated {
		t.Fatalf("parse unexpectedly truncated: %s", rt.Summary())
	}
	if got, want := root.Type(lang), "source_file"; got != want {
		t.Fatalf("root type = %q, want %q; tree=%s", got, want, root.SExpr(lang))
	}
	if got, want := root.EndByte(), uint32(len(src)); got != want {
		t.Fatalf("root end = %d, want %d; tree=%s", got, want, root.SExpr(lang))
	}
	if !root.HasError() {
		t.Fatalf("root should retain recovery error for malformed godebug directive; tree=%s", root.SExpr(lang))
	}

	require := findFirstGomodNodeOfType(root, lang, "require_directive")
	if require == nil {
		t.Fatalf("missing require_directive after recovered godebug directive; tree=%s", root.SExpr(lang))
	}
	if got, want := require.EndByte(), uint32(len(src)); got != want {
		t.Fatalf("require_directive end = %d, want %d; tree=%s", got, want, root.SExpr(lang))
	}
}

func TestGomodRequireListStaysSingleStack(t *testing.T) {
	src := []byte("module example.com/m\n\ngo 1.26.0\n\nrequire (\n\texample.com/a v1.2.3\n\texample.com/b v1.2.4\n\texample.com/c v1.2.5\n)\n")
	lang := GomodLanguage()
	parser := gotreesitter.NewParser(lang)
	parser.SetAdmissionCandidateRoute(false)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	defer tree.Release()

	root := tree.RootNode()
	if root == nil {
		t.Fatal("parse returned nil root")
	}
	if root.HasError() {
		t.Fatalf("valid gomod require list has parse errors: %s", root.SExpr(lang))
	}
	if got, want := root.EndByte(), uint32(len(src)); got != want {
		t.Fatalf("root end = %d, want %d; tree=%s", got, want, root.SExpr(lang))
	}
	if rt := tree.ParseRuntime(); rt.MaxStacksSeen != 1 {
		t.Fatalf("MaxStacksSeen = %d, want 1; runtime=%s", rt.MaxStacksSeen, rt.Summary())
	}
}

func findFirstGomodNodeOfType(node *gotreesitter.Node, lang *gotreesitter.Language, typ string) *gotreesitter.Node {
	if node == nil {
		return nil
	}
	if node.Type(lang) == typ {
		return node
	}
	for i := 0; i < node.ChildCount(); i++ {
		if found := findFirstGomodNodeOfType(node.Child(i), lang, typ); found != nil {
			return found
		}
	}
	return nil
}
