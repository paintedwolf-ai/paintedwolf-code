//go:build grammar_subset && grammar_subset_purescript

package grammars

func init() {
	RegisterExternalScanner("purescript", PurescriptExternalScanner{})
}
