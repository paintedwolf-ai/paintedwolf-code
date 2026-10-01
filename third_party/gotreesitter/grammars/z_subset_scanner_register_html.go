//go:build grammar_subset && grammar_subset_html

package grammars

func init() {
	RegisterExternalScanner("html", HTMLExternalScanner{})
}
