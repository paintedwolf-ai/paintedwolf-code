package grammargen

import (
	"os"
	"testing"
	"time"

	"github.com/odvcencio/gotreesitter/grammars"
)

func TestRustClosureOrPatternParity(t *testing.T) {
	jsonPath := rustGrammarJSONPathForTest(t)
	source, err := os.ReadFile(jsonPath)
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

	cases := []struct {
		name string
		src  string
	}{
		{name: "let_tuple_closure", src: "fn f() { let (|x| x) = y; }\n"},
		{name: "let_tuple_captured", src: "fn f() { let (|__@_|__) = y; }\n"},
		{name: "expr_tuple_captured", src: "fn f() { let val = ((|__@_|__)); }\n"},
		{name: "tuple_param_nested", src: "fn f() { let val = ((|(..):(_,_),(|__@_|__)|__)); }\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertGeneratedAndReferenceDeepParity(t, genLang, refLang, tc.src)
		})
	}
}
