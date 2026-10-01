//go:build grammar_subset && grammar_subset_vue

package grammars

func init() {
	RegisterExternalScanner("vue", VueExternalScanner{})
}
