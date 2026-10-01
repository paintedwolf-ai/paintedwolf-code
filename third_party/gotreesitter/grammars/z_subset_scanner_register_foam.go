//go:build grammar_subset && grammar_subset_foam

package grammars

func init() {
	RegisterExternalScanner("foam", FoamExternalScanner{})
}
