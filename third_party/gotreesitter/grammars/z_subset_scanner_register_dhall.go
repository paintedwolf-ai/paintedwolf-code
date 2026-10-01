//go:build grammar_subset && grammar_subset_dhall

package grammars

func init() {
	RegisterExternalScanner("dhall", DhallExternalScanner{})
}
