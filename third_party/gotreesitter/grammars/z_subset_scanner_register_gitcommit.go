//go:build grammar_subset && grammar_subset_gitcommit

package grammars

func init() {
	RegisterExternalScanner("gitcommit", GitcommitExternalScanner{})
}
