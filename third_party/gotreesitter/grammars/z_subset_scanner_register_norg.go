//go:build grammar_subset && grammar_subset_norg

package grammars

func init() {
	RegisterExternalScanner("norg", NorgExternalScanner{})
}
