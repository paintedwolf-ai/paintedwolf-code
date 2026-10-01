package gotreesitter_test

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// BenchmarkGoParseFreshParserPerFileDFA measures the admission Parse path when a
// caller creates a fresh Parser for every file (a common per-request pattern).
// It guards the per-*Language table cache: the candidate route must not rebuild
// the compact action tables on each fresh Parser. Set GTS_ADMISSION_CANDIDATE=0
// to measure the production route for the same interleaved comparison.
func BenchmarkGoParseFreshParserPerFileDFA(b *testing.B) {
	lang := grammars.GoLanguage()
	src := makeGoBenchmarkSource(benchmarkFuncCount(b))

	// Warm the per-language table cache once so the steady-state per-file cost,
	// not the one-time first-build, is what the loop measures.
	warm := gotreesitter.NewParser(lang)
	if tree, err := warm.Parse(src); err != nil {
		b.Fatalf("warm parse error: %v", err)
	} else {
		tree.Release()
	}

	b.ReportAllocs()
	b.SetBytes(int64(len(src)))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		parser := gotreesitter.NewParser(lang)
		tree, err := parser.Parse(src)
		if err != nil {
			b.Fatalf("parse error: %v", err)
		}
		requireCompleteParse(b, tree, src, lang, "fresh parser full dfa")
		tree.Release()
	}
}
