package grammars_test

import (
	"testing"

	"github.com/odvcencio/gotreesitter/grammars"
)

// BenchmarkDetectLanguage measures the hot path for extension-based detection.
// With 206 registered languages, the O(n) scan was measurable in server workloads
// that detect many files per request; the extIndex makes it O(1).
func BenchmarkDetectLanguage(b *testing.B) {
	// Common extensions — warm path.
	filenames := []string{
		"main.go",
		"index.ts",
		"app.py",
		"Makefile",
		"README.md",
		"styles.css",
		"main.rs",
		"server.js",
		"config.yaml",
		"build.gradle",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, f := range filenames {
			_ = grammars.DetectLanguage(f)
		}
	}
}

// BenchmarkDetectLanguageUnknown measures the cold/miss path for an unregistered extension.
func BenchmarkDetectLanguageUnknown(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = grammars.DetectLanguage("data.zzz_unknown_ext")
	}
}
