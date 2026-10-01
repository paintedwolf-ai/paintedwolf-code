//go:build grammar_subset && grammar_subset_bicep

package grammars

func init() {
	RegisterExternalScanner("bicep", BicepExternalScanner{})
}
