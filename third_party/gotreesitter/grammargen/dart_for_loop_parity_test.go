package grammargen

import (
	"testing"
	"time"

	"github.com/odvcencio/gotreesitter"
)

func TestGeneratedDartForLoopRelationalConditionMatchesReference(t *testing.T) {
	spec, ok := importParityGrammarByName("dart")
	if !ok {
		t.Fatal("missing dart import parity grammar")
	}
	gram, err := importParityGrammarSource(spec)
	if err != nil {
		t.Skipf("Dart grammar source unavailable: %v", err)
	}
	lang, err := generateWithTimeout(gram, 90*time.Second)
	if err != nil {
		t.Fatalf("generate Dart language: %v", err)
	}
	refLang := spec.blobFunc()
	adaptExternalScanner(refLang, lang)

	src := []byte("void main() { for (var i = 0; i < 10; i++) {} }\n")
	assertDartParseClean(t, "reference", refLang, src)
	assertDartParseClean(t, "generated", lang, src)
}

func TestGeneratedDartSequentialRelationalStatementsMatchReference(t *testing.T) {
	spec, ok := importParityGrammarByName("dart")
	if !ok {
		t.Fatal("missing dart import parity grammar")
	}
	gram, err := importParityGrammarSource(spec)
	if err != nil {
		t.Skipf("Dart grammar source unavailable: %v", err)
	}
	lang, err := generateWithTimeout(gram, 90*time.Second)
	if err != nil {
		t.Fatalf("generate Dart language: %v", err)
	}
	refLang := spec.blobFunc()
	adaptExternalScanner(refLang, lang)

	for _, tc := range []struct {
		name string
		src  []byte
	}{
		{name: "empty body", src: []byte("main() {}\n")},
		{name: "one statement", src: []byte("main() {\n  a > b;\n}\n")},
		{name: "two statements", src: []byte("main() {\n  a > b;\n  a < b;\n}\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertDartParseClean(t, "reference", refLang, tc.src)
			assertDartParseClean(t, "generated", lang, tc.src)
		})
	}
}

func assertDartParseClean(t *testing.T, label string, lang *gotreesitter.Language, src []byte) {
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
		t.Fatalf("%s parser produced errors: %s", label, root.SExpr(lang))
	}
}

func importParityGrammarByName(name string) (importParityGrammar, bool) {
	for _, spec := range importParityGrammars {
		if spec.name == name {
			return spec, true
		}
	}
	return importParityGrammar{}, false
}
