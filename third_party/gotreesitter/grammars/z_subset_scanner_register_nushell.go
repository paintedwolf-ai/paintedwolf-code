//go:build grammar_subset && grammar_subset_nushell

package grammars

func init() {
	RegisterExternalScanner("nushell", NushellExternalScanner{})
}
