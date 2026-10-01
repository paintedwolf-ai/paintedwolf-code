package grammargen

import (
	"testing"
	"time"

	"github.com/odvcencio/gotreesitter"
)

func TestGeneratedDartSwitchObjectPatternTypeIdentifierMatchesReference(t *testing.T) {
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

	src := []byte(`void f() {
  switch (m) {
    case C(:final double x):
      break;
  }
}
`)

	genTree := parseDartSwitchObjectPatternSource(t, genLang, src, "generated")
	refTree := parseDartSwitchObjectPatternSource(t, refLang, src, "reference")
	divs := compareTreesDeep(genTree.RootNode(), genLang, refTree.RootNode(), refLang, "root", 20)
	if len(divs) > 0 {
		t.Fatalf("generated/reference Dart switch object pattern mismatch: %s\ngen=%s\nref=%s",
			divs[0], genTree.RootNode().SExpr(genLang), refTree.RootNode().SExpr(refLang))
	}
}

func parseDartSwitchObjectPatternSource(t *testing.T, lang *gotreesitter.Language, src []byte, label string) *gotreesitter.Tree {
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
