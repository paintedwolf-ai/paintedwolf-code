//go:build grammar_subset && grammar_subset_agda

package grammars

func init() {
	RegisterExternalScanner("agda", AgdaExternalScanner{})
}
