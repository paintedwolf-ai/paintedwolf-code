package grammargen

import (
	"os"
	"testing"
	"time"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func TestRustDocCommentGLRNoLookaheadParity(t *testing.T) {
	source, err := os.ReadFile(rustGrammarJSONPathForTest(t))
	if err != nil {
		t.Skipf("Rust grammar.json not available: %v", err)
	}
	gram, err := ImportGrammarJSON(source)
	if err != nil {
		t.Fatalf("import Rust grammar.json: %v", err)
	}
	genLang, err := generateWithTimeout(gram, 90*time.Second)
	if err != nil {
		t.Fatalf("generate Rust language: %v", err)
	}
	refLang := grammars.RustLanguage()
	adaptExternalScanner(refLang, genLang)

	src, err := os.ReadFile("/tmp/grammar_parity/rust/examples/ast.rs")
	if err != nil {
		t.Skipf("Rust ast.rs not available: %v", err)
	}
	if len(src) < 1600 {
		t.Fatalf("Rust ast.rs fixture too short: %d", len(src))
	}
	src = src[:1600]

	genParser := gotreesitter.NewParser(genLang)
	refParser := gotreesitter.NewParser(refLang)
	genTree, _ := genParser.Parse(src)
	refTree, _ := refParser.Parse(src)
	defer genTree.Release()
	defer refTree.Release()

	genRoot := genTree.RootNode()
	refRoot := refTree.RootNode()
	if genRoot.EndByte() != refRoot.EndByte() ||
		genRoot.HasError() != refRoot.HasError() ||
		genRoot.ChildCount() != refRoot.ChildCount() ||
		genTree.ParseRuntime().TokensConsumed != refTree.ParseRuntime().TokensConsumed {
		t.Fatalf("generated Rust doc-comment prefix diverged: gen end=%d err=%t children=%d runtime=%s ref end=%d err=%t children=%d runtime=%s",
			genRoot.EndByte(), genRoot.HasError(), genRoot.ChildCount(), genTree.ParseRuntime().Summary(),
			refRoot.EndByte(), refRoot.HasError(), refRoot.ChildCount(), refTree.ParseRuntime().Summary())
	}
}
