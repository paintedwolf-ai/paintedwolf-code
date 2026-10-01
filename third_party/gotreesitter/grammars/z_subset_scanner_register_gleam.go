//go:build grammar_subset && grammar_subset_gleam

package grammars

func init() {
	RegisterExternalScanner("gleam", GleamExternalScanner{})
}
