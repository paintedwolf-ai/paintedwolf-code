package main

import "testing"

// BenchmarkParseCorpus parses the same fixed multi-grammar corpus
// (go, c_sharp, bash, python, cmake under cgo_harness/corpus_real) used to
// collect the production component of the PGO profile. It is built without
// PGO and with candidate profiles to measure build-time PGO on an established
// representative parsing workload.
func BenchmarkParseCorpus(b *testing.B) {
	root, err := findCorpusRoot()
	if err != nil {
		b.Fatalf("find corpus root: %v", err)
	}
	files, err := loadCorpus(root)
	if err != nil {
		b.Fatalf("load corpus: %v", err)
	}
	parsers := buildParsers()

	// Warm up so grammar table construction isn't counted.
	if _, err := parseOnce(parsers, files); err != nil {
		b.Fatalf("warmup: %v", err)
	}

	var totalBytes int64
	for _, f := range files {
		totalBytes += int64(len(f.src))
	}
	b.SetBytes(totalBytes)
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := parseOnce(parsers, files); err != nil {
			b.Fatalf("parse: %v", err)
		}
	}
}
