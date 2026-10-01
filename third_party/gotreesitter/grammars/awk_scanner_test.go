//go:build !grammar_subset || grammar_subset_awk

package grammars

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func TestParseAwkBareBackslashProgramSpansEOFLikeC(t *testing.T) {
	src := []byte("\\")
	lang := AwkLanguage()
	parser := gotreesitter.NewParser(lang)

	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	t.Cleanup(tree.Release)

	root := tree.RootNode()
	if root == nil {
		t.Fatal("Parse returned nil root")
	}
	if got, want := root.Type(lang), "program"; got != want {
		t.Fatalf("root type = %q, want %q", got, want)
	}
	if root.HasError() {
		t.Fatalf("root.HasError = true, want false: %s", root.SExpr(lang))
	}
	if got, want := root.StartByte(), uint32(len(src)); got != want {
		t.Fatalf("root.StartByte = %d, want %d", got, want)
	}
	if got, want := root.EndByte(), uint32(len(src)); got != want {
		t.Fatalf("root.EndByte = %d, want %d", got, want)
	}
	rt := tree.ParseRuntime()
	if rt.Truncated {
		t.Fatalf("ParseRuntime.Truncated = true: %s", rt.Summary())
	}
}
