package grammargen

import (
	"strings"
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func TestKeywordFollowTokensAllowKeywordAfterReduce(t *testing.T) {
	g := NewGrammar("keyword_follow_tokens")

	g.Define("source_file", Seq(Sym("name"), Sym("alias_clause")))
	g.Define("name", Sym("identifier"))
	g.Define("alias_clause", Seq(Str("AS"), Sym("identifier")))
	g.Define("identifier", Pat(`[A-Za-z_][A-Za-z0-9_]*`))
	g.SetWord("identifier")
	g.SetExtras(Pat(`\s`))

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage failed: %v", err)
	}

	tree, err := gotreesitter.NewParser(lang).Parse([]byte("foo AS bar"))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("nil root")
	}
	sexpr := root.SExpr(lang)
	if strings.Contains(sexpr, "ERROR") {
		t.Fatalf("unexpected parse error: %s", sexpr)
	}
	if !strings.Contains(sexpr, "alias_clause") {
		t.Fatalf("missing alias_clause after reduce-follow keyword: %s", sexpr)
	}
}
