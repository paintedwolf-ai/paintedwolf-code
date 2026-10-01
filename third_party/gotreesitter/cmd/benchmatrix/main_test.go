package main

import "testing"

func TestParseBenchOutputKeepsFamilySubBenchmarkNames(t *testing.T) {
	out := []byte(`BenchmarkFamilyParseFullDFA/systems/go-8          1000  12345 ns/op  123.4 MB/s  64 B/op  2 allocs/op
BenchmarkFamilyParseFullDFA/web/typescript-8       1000  23456 ns/op   98.7 MB/s  96 B/op  3 allocs/op
`)
	got, err := parseBenchOutput(out)
	if err != nil {
		t.Fatalf("parseBenchOutput failed: %v", err)
	}
	for _, name := range []string{
		"BenchmarkFamilyParseFullDFA/systems/go",
		"BenchmarkFamilyParseFullDFA/web/typescript",
	} {
		if len(got[name]) != 1 {
			t.Fatalf("expected one sample for %s, got %#v", name, got[name])
		}
	}
	if got["BenchmarkFamilyParseFullDFA/systems/go"][0].MBPerSec != 123.4 {
		t.Fatalf("unexpected MB/s for systems/go: %#v", got["BenchmarkFamilyParseFullDFA/systems/go"][0])
	}
}
