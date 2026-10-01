//go:build grammar_subset && grammar_subset_less

package grammars

func init() {
	RegisterExternalScanner("less", LessExternalScanner{})
}
