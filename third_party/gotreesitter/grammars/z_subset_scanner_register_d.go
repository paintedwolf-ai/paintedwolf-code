//go:build grammar_subset && grammar_subset_d

package grammars

func init() {
	RegisterExternalScanner("d", DExternalScanner{})
}
