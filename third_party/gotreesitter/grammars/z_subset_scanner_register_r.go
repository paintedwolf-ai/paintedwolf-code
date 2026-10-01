//go:build grammar_subset && grammar_subset_r

package grammars

func init() {
	RegisterExternalScanner("r", RExternalScanner{})
}
