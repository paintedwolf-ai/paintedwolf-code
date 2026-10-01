package grammargen

import (
	"os"
	"testing"
	"time"

	"github.com/odvcencio/gotreesitter/grammars"
)

func TestRustContinueBreakLabelParity(t *testing.T) {
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
		{name: "continue_label", src: "fn f() { loop { continue 'outer; } }\n"},
		{name: "break_label", src: "fn f() { loop { break 'outer; } }\n"},
		{name: "labeled_loop_continue", src: "fn f() { 'outer: loop { continue 'outer; } }\n"},
		{name: "labeled_loop_break", src: "fn f() { 'outer: loop { break 'outer; } }\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertGeneratedAndReferenceDeepParity(t, genLang, refLang, tc.src)
		})
	}
}
