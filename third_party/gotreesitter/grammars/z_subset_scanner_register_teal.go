//go:build grammar_subset && grammar_subset_teal

package grammars

func init() {
	RegisterExternalScanner("teal", TealExternalScanner{})
}
