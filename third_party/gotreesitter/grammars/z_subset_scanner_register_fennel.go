//go:build grammar_subset && grammar_subset_fennel

package grammars

func init() {
	RegisterExternalScanner("fennel", FennelExternalScanner{})
}
