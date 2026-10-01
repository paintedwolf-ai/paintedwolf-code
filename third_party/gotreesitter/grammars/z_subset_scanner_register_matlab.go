//go:build grammar_subset && grammar_subset_matlab

package grammars

func init() {
	RegisterExternalScanner("matlab", MatlabExternalScanner{})
}
