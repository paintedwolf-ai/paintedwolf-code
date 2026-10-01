//go:build grammar_subset && grammar_subset_odin

package grammars

func init() {
	RegisterExternalScanner("odin", OdinExternalScanner{})
}
