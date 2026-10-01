//go:build grammar_subset && grammar_subset_djot

package grammars

func init() {
	RegisterExternalScanner("djot", DjotExternalScanner{})
}
