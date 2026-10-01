//go:build grammar_subset && grammar_subset_css

package grammars

func init() {
	RegisterExternalScanner("css", CssExternalScanner{})
}
