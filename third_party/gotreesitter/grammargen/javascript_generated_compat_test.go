package grammargen

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func TestGeneratedJavaScriptOptionalChainMatchesCLeafShape(t *testing.T) {
	lang, err := GenerateLanguage(JavascriptGrammar())
	if err != nil {
		t.Fatalf("GenerateLanguage(JavaScript): %v", err)
	}
	if lang.Name != "javascript" {
		t.Fatalf("Language.Name = %q, want javascript", lang.Name)
	}

	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse([]byte("a?.b;\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()

	node := firstDescendantOfType(tree.RootNode(), lang, "optional_chain")
	if node == nil {
		t.Fatalf("missing optional_chain: %s", tree.RootNode().SExpr(lang))
	}
	if got, want := node.ChildCount(), 0; got != want {
		t.Fatalf("optional_chain child count = %d, want %d; root=%s", got, want, tree.RootNode().SExpr(lang))
	}
}

func firstDescendantOfType(root *gotreesitter.Node, lang *gotreesitter.Language, typ string) *gotreesitter.Node {
	if root == nil {
		return nil
	}
	if root.Type(lang) == typ {
		return root
	}
	for i := 0; i < root.ChildCount(); i++ {
		if found := firstDescendantOfType(root.Child(i), lang, typ); found != nil {
			return found
		}
	}
	return nil
}
