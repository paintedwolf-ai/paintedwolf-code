package grammars

import (
	"strings"
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func TestEmbeddedCSharpNativeUnicodeIdentifier(t *testing.T) {
	lang := CSharpLanguage()
	source := []byte("class C { int ග්‍රහලෝකය = 0; }\n")
	tree := parseEmbeddedCSharpNativeFixture(t, lang, source)
	defer tree.Release()

	identifier := findFirstNamedDescendantWhere(tree.RootNode(), lang, "identifier", func(node *gotreesitter.Node) bool {
		return node.Text(source) == "ග්‍රහලෝකය"
	})
	if identifier == nil {
		t.Fatalf("missing full Unicode identifier span: %s", tree.RootNode().SExpr(lang))
	}
}

func TestEmbeddedCSharpNativeScopedLambdaBlock(t *testing.T) {
	lang := CSharpLanguage()
	source := []byte(`class C {
    void M() {
        var first = scoped => null;
        var second = (scoped value) => value;
    }
}
`)
	tree := parseEmbeddedCSharpNativeFixture(t, lang, source)
	defer tree.Release()

	if got := countNamedCSharpNodes(tree.RootNode(), lang, "lambda_expression"); got != 2 {
		t.Fatalf("lambda_expression count = %d, want 2: %s", got, tree.RootNode().SExpr(lang))
	}
	if got := countNamedCSharpNodes(tree.RootNode(), lang, "local_declaration_statement"); got != 2 {
		t.Fatalf("local_declaration_statement count = %d, want 2: %s", got, tree.RootNode().SExpr(lang))
	}
}

func TestEmbeddedCSharpNativeLINQQueryExpression(t *testing.T) {
	lang := CSharpLanguage()
	source := []byte(`class C {
    void M() {
        var result = from a in sourceA
                     join b in sourceB on a.ID equals b.ID
                     where a.Enabled
                     select new { a, b };
    }
}
`)
	tree := parseEmbeddedCSharpNativeFixture(t, lang, source)
	defer tree.Release()

	query := findFirstNamedDescendantWhere(tree.RootNode(), lang, "query_expression", func(node *gotreesitter.Node) bool {
		return strings.Contains(node.Text(source), "join b in sourceB")
	})
	if query == nil {
		t.Fatalf("missing native query_expression: %s", tree.RootNode().SExpr(lang))
	}
}

func parseEmbeddedCSharpNativeFixture(t *testing.T, lang *gotreesitter.Language, source []byte) *gotreesitter.Tree {
	t.Helper()
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(source)
	if err != nil {
		t.Fatalf("parse embedded C# fixture: %v", err)
	}
	if tree == nil || tree.RootNode() == nil {
		t.Fatal("parse embedded C# fixture returned nil root")
	}
	root := tree.RootNode()
	if root.HasError() || root.EndByte() != uint32(len(source)) {
		t.Fatalf("embedded C# fixture was not accepted cleanly to EOF: %s", root.SExpr(lang))
	}
	return tree
}

func countNamedCSharpNodes(node *gotreesitter.Node, lang *gotreesitter.Language, typ string) int {
	if node == nil {
		return 0
	}
	count := 0
	if node.IsNamed() && node.Type(lang) == typ {
		count++
	}
	for i := 0; i < node.NamedChildCount(); i++ {
		count += countNamedCSharpNodes(node.NamedChild(i), lang, typ)
	}
	return count
}
