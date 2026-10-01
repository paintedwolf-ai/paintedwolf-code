//go:build grammar_subset && grammar_subset_rst

package grammars

func init() {
	RegisterExternalScanner("rst", RstExternalScanner{})
}
