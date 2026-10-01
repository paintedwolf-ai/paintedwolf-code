//go:build !grammar_subset || grammar_subset_cobol

package grammars

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestCobolFixedFormatCommentCorpusBlockParsesClean(t *testing.T) {
	src := []byte("aaaaaa identification division.\n" +
		"aaaaaa program-id. a.  ,,, ;;;                                          aaaaa\n" +
		"      *aaaa\n")

	tree, err := gotreesitter.NewParser(CobolLanguage()).Parse(src)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if tree == nil || tree.RootNode() == nil {
		t.Fatal("Parse returned nil tree/root")
	}
	if tree.RootNode().HasError() {
		t.Fatalf("COBOL corpus block parsed with error: %s", tree.RootNode().SExpr(CobolLanguage()))
	}
	if got, want := tree.RootNode().Type(CobolLanguage()), "start"; got != want {
		t.Fatalf("root type = %q, want %q", got, want)
	}
}

func TestCobolDateWrittenCommentEntryParsesClean(t *testing.T) {
	lang := CobolLanguage()
	src := []byte("       IDENTIFICATION DIVISION.\n" +
		"       PROGRAM-ID. BBANK10P.\n" +
		"       DATE-WRITTEN.\n" +
		"       September 2002.\n")

	tree, err := gotreesitter.NewParser(lang).Parse(src)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if tree == nil || tree.RootNode() == nil {
		t.Fatal("Parse returned nil tree/root")
	}
	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("COBOL date-written section parsed with error: %s", root.SExpr(lang))
	}
	if got, want := tree.ParseStopReason(), gotreesitter.ParseStopAccepted; got != want {
		t.Fatalf("stop = %s, want %s; runtime=%s", got, want, tree.ParseRuntime().Summary())
	}

	var found bool
	var walk func(*gotreesitter.Node)
	walk = func(n *gotreesitter.Node) {
		if n == nil || found {
			return
		}
		if n.Type(lang) == "date_written_section" {
			found = true
			return
		}
		for i := 0; i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	if !found {
		t.Fatalf("missing date_written_section: %s", root.SExpr(lang))
	}
}
