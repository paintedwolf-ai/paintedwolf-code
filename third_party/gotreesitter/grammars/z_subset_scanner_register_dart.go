//go:build grammar_subset && grammar_subset_dart

package grammars

func init() {
	RegisterExternalScanner("dart", DartExternalScanner{})
}
