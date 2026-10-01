package grammargen

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// Verifies that SwiftGrammar composes through ExtendGrammar and produces a
// runnable Language that parses trivial Swift.
func TestSwiftGrammarExtendSmoke(t *testing.T) {
	skipHeavyGrammarExtendSmokeUnderRace(t, "Swift")

	g := ExtendGrammar("swift_smoke", SwiftGrammar(), func(g *Grammar) {})
	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse([]byte(`func f() {}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	defer tree.Release()
	if tree.RootNode().HasError() {
		t.Fatalf("parse error in trivial Swift: %s", tree.RootNode().SExpr(lang))
	}
}

func TestSwiftGrammarBlobParity(t *testing.T) {
	skipHeavyGrammarExtendSmokeUnderRace(t, "Swift")

	genLang, err := GenerateLanguage(SwiftGrammar())
	if err != nil {
		t.Fatalf("compile generated Swift grammar: %v", err)
	}
	refLang := grammars.SwiftLanguage()
	adaptExternalScanner(refLang, genLang)

	samples := []string{
		"func f() {}\n",
		"struct Box<T> { let value: T }\n",
	}
	for _, sample := range samples {
		genTree, err := gotreesitter.NewParser(genLang).Parse([]byte(sample))
		if err != nil {
			t.Fatalf("parse generated Swift: %v", err)
		}
		refTree, err := gotreesitter.NewParser(refLang).Parse([]byte(sample))
		if err != nil {
			genTree.Release()
			t.Fatalf("parse reference Swift: %v", err)
		}

		genRoot := genTree.RootNode()
		refRoot := refTree.RootNode()
		genSexp := genRoot.SExpr(genLang)
		refSexp := refRoot.SExpr(refLang)

		if genRoot.HasError() || refRoot.HasError() {
			genTree.Release()
			refTree.Release()
			t.Fatalf("error mismatch for %q\nGEN hasError=%v\nGEN: %s\nREF hasError=%v\nREF: %s",
				sample, genRoot.HasError(), genSexp, refRoot.HasError(), refSexp)
		}
		if genSexp != refSexp {
			genTree.Release()
			refTree.Release()
			t.Fatalf("SExpr mismatch for %q\nGEN: %s\nREF: %s", sample, genSexp, refSexp)
		}
		if divs := compareTreesDeep(genRoot, genLang, refRoot, refLang, "root", 10); len(divs) > 0 {
			genTree.Release()
			refTree.Release()
			t.Fatalf("deep mismatch for %q: %s\nGEN: %s\nREF: %s", sample, divs[0].String(), genSexp, refSexp)
		}
		genTree.Release()
		refTree.Release()
	}
}

// Verifies that KotlinGrammar composes through ExtendGrammar and produces a
// runnable Language that parses trivial Kotlin.
func TestKotlinGrammarExtendSmoke(t *testing.T) {
	skipHeavyGrammarExtendSmokeUnderRace(t, "Kotlin")

	g := ExtendGrammar("kotlin_smoke", KotlinGrammar(), func(g *Grammar) {})
	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse([]byte(`val x = 1`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	defer tree.Release()
	if tree.RootNode().HasError() {
		t.Fatalf("parse error in trivial Kotlin: %s", tree.RootNode().SExpr(lang))
	}
}

func skipHeavyGrammarExtendSmokeUnderRace(t *testing.T, grammarName string) {
	t.Helper()
	if raceEnabled {
		t.Skipf("skipping full %s grammar generation under -race; non-race parity and constructor tests cover this path", grammarName)
	}
}
