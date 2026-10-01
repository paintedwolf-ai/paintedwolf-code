package grammargen

import (
	"testing"
	"time"

	"github.com/odvcencio/gotreesitter"
)

func TestGeneratedDartTypeCastPrecedenceMatchesReference(t *testing.T) {
	spec, ok := importParityGrammarByName("dart")
	if !ok {
		t.Fatal("missing dart import parity grammar")
	}
	gram, err := importParityGrammarSource(spec)
	if err != nil {
		t.Skipf("Dart grammar source unavailable: %v", err)
	}
	genLang, err := generateDartParityLanguageWithTimeout(gram, 2*time.Minute)
	if err != nil {
		t.Fatalf("generate Dart: %v", err)
	}
	refLang := spec.blobFunc()
	adaptExternalScanner(refLang, genLang)

	src := []byte(`void main() {
  a < b['json'] as BigB;
  a == b as BigB;
  a && b as BigB;
  a as BigB || b as BigB;
  if (a['json'] as BigB < b as BigB) {}
  a as BigB | b as BigB;
}
`)

	genTree := parseDartCastPrecedenceSource(t, genLang, src, "generated")
	refTree := parseDartCastPrecedenceSource(t, refLang, src, "reference")
	divs := compareTreesDeep(genTree.RootNode(), genLang, refTree.RootNode(), refLang, "root", 20)
	if len(divs) > 0 {
		t.Fatalf("generated/reference Dart cast precedence mismatch: %s\ngen=%s\nref=%s",
			divs[0], genTree.RootNode().SExpr(genLang), refTree.RootNode().SExpr(refLang))
	}
}

func parseDartCastPrecedenceSource(t *testing.T, lang *gotreesitter.Language, src []byte, label string) *gotreesitter.Tree {
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
