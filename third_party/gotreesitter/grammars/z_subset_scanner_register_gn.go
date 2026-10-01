//go:build grammar_subset && grammar_subset_gn

package grammars

func init() {
	RegisterExternalScanner("gn", GnExternalScanner{})
}
