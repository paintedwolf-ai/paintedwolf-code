//go:build grammar_subset && grammar_subset_pkl

package grammars

func init() {
	RegisterExternalScanner("pkl", PklExternalScanner{})
}
