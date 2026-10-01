package grammargen

import (
	"os"
	"testing"
	"time"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func TestRegexImportCharacterClassRangeParity(t *testing.T) {
	const grammarPath = "/tmp/grammar_parity/regex/src/grammar.json"
	source, err := os.ReadFile(grammarPath)
	if err != nil {
		t.Skipf("regex grammar.json not available: %v", err)
	}
	gram, err := ImportGrammarJSON(source)
	if err != nil {
		t.Fatalf("import regex grammar: %v", err)
	}
	gram.BinaryRepeatMode = true

	genLang, err := generateWithTimeout(gram, 90*time.Second)
	if err != nil {
		t.Fatalf("generate regex language: %v", err)
	}
	refLang := grammars.RegexLanguage()

	for _, sample := range []string{
		"[a-z]",
		"(?<year>[0-9]{4})-(?<month>[0-9]{2})-(?<day>[0-9]{2})\n",
	} {
		t.Run(sample, func(t *testing.T) {
			src := []byte(sample)
			genTree, err := gotreesitter.NewParser(genLang).Parse(src)
			if err != nil {
				t.Fatalf("generated parse: %v", err)
			}
			defer genTree.Release()
			refTree, err := gotreesitter.NewParser(refLang).Parse(src)
			if err != nil {
				t.Fatalf("reference parse: %v", err)
			}
			defer refTree.Release()

			genRoot := genTree.RootNode()
			refRoot := refTree.RootNode()
			if got, want := genRoot.EndByte(), uint32(len(src)); got != want {
				t.Fatalf("generated root.EndByte=%d want %d runtime=%s gen=%s ref=%s",
					got, want, genTree.ParseRuntime().Summary(), genRoot.SExpr(genLang), refRoot.SExpr(refLang))
			}
			if genRoot.SExpr(genLang) != refRoot.SExpr(refLang) {
				t.Fatalf("generated/reference SExpr mismatch\n  gen: %s\n  ref: %s",
					genRoot.SExpr(genLang), refRoot.SExpr(refLang))
			}
		})
	}
}
