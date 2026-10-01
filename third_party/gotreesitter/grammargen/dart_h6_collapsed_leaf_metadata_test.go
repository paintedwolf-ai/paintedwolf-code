package grammargen

import (
	"testing"
	"time"

	"github.com/odvcencio/gotreesitter"
)

func TestDartH6GeneratedCollapsedLeafMetadataAndMaterialization(t *testing.T) {
	spec, ok := importParityGrammarByName("dart")
	if !ok {
		t.Fatal("missing dart import parity grammar")
	}
	gram, err := importParityGrammarSource(spec)
	if err != nil {
		t.Skipf("Dart grammar source unavailable: %v", err)
	}
	genLang, err := generateWithTimeout(gram, 2*time.Minute)
	if err != nil {
		t.Fatalf("generate Dart: %v", err)
	}
	if got, want := genLang.Name, "dart"; got != want {
		t.Fatalf("generated language name = %q, want %q", got, want)
	}
	assertAnonymousVisibleSymbol(t, genLang, "?")
	assertAnonymousVisibleSymbol(t, genLang, "null")

	refLang := spec.blobFunc()
	adaptExternalScanner(refLang, genLang)

	src := []byte(`
class Parser {
  Tree parse(String program, {int? encoding}) {
    if (program == null) {
      return Tree();
    }
    return Tree();
  }
}
`)
	genTree := parseDartH6CollapsedLeafSource(t, genLang, src, "generated")
	refTree := parseDartH6CollapsedLeafSource(t, refLang, src, "reference")
	assertDartH6CollapsedLeafShape(t, genLang, genTree.RootNode(), "generated")
	assertDartH6CollapsedLeafShape(t, refLang, refTree.RootNode(), "reference")

	divs := compareTreesDeep(genTree.RootNode(), genLang, refTree.RootNode(), refLang, "root", 20)
	if len(divs) > 0 {
		t.Fatalf("generated/reference Dart mismatch after collapsed-leaf repair: %s\ngen=%s\nref=%s",
			divs[0], genTree.RootNode().SExpr(genLang), refTree.RootNode().SExpr(refLang))
	}
}

func assertAnonymousVisibleSymbol(t *testing.T, lang *gotreesitter.Language, name string) {
	t.Helper()
	sym, ok := lang.SymbolByName(name)
	if !ok {
		t.Fatalf("SymbolByName(%q) missing", name)
	}
	if int(sym) >= len(lang.SymbolMetadata) {
		t.Fatalf("SymbolByName(%q) = %d outside metadata len %d", name, sym, len(lang.SymbolMetadata))
	}
	meta := lang.SymbolMetadata[sym]
	if meta.Name != name || !meta.Visible || meta.Named {
		t.Fatalf("SymbolByName(%q) metadata = {Name:%q Visible:%v Named:%v}, want anonymous visible",
			name, meta.Name, meta.Visible, meta.Named)
	}
}

func parseDartH6CollapsedLeafSource(t *testing.T, lang *gotreesitter.Language, src []byte, label string) *gotreesitter.Tree {
	t.Helper()
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("%s parse failed: %v", label, err)
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatalf("%s parse missing root node", label)
	}
	if root.HasError() {
		t.Fatalf("%s parse has error: %s", label, root.SExpr(lang))
	}
	return tree
}

func assertDartH6CollapsedLeafShape(t *testing.T, lang *gotreesitter.Language, root *gotreesitter.Node, label string) {
	t.Helper()
	var nullableNodes []*gotreesitter.Node
	var nullNodes []*gotreesitter.Node
	var walk func(*gotreesitter.Node)
	walk = func(n *gotreesitter.Node) {
		if n == nil {
			return
		}
		switch n.Type(lang) {
		case "nullable_type":
			nullableNodes = append(nullableNodes, n)
		case "null_literal":
			nullNodes = append(nullNodes, n)
		}
		for i := 0; i < n.ChildCount(); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
	if got, want := len(nullableNodes), 1; got != want {
		t.Fatalf("%s nullable_type count = %d, want %d; tree=%s", label, got, want, root.SExpr(lang))
	}
	assertDartH6SingleAnonymousChild(t, lang, nullableNodes[0], "?", label)
	if got, want := len(nullNodes), 1; got != want {
		t.Fatalf("%s null_literal count = %d, want %d; tree=%s", label, got, want, root.SExpr(lang))
	}
	assertDartH6SingleAnonymousChild(t, lang, nullNodes[0], "null", label)
}

func assertDartH6SingleAnonymousChild(t *testing.T, lang *gotreesitter.Language, node *gotreesitter.Node, wantType, label string) {
	t.Helper()
	if got := node.ChildCount(); got != 1 {
		t.Fatalf("%s %s child count = %d, want 1; node=%s", label, node.Type(lang), got, node.SExpr(lang))
	}
	child := node.Child(0)
	if child == nil {
		t.Fatalf("%s %s missing child", label, node.Type(lang))
	}
	if child.Type(lang) != wantType || child.IsNamed() {
		t.Fatalf("%s %s child type/named = %q/%v, want %q/false; node=%s",
			label, node.Type(lang), child.Type(lang), child.IsNamed(), wantType, node.SExpr(lang))
	}
}
