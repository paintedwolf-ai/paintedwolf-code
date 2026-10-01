//go:build grammar_subset && grammar_subset_tlaplus

package grammars

func init() {
	RegisterExternalScanner("tlaplus", TlaplusExternalScanner{})
}
