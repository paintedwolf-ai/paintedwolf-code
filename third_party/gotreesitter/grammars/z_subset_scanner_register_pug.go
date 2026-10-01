//go:build grammar_subset && grammar_subset_pug

package grammars

func init() {
	RegisterExternalScanner("pug", PugExternalScanner{})
}
