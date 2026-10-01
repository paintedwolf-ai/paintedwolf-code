//go:build grammar_subset && grammar_subset_just

package grammars

func init() {
	RegisterExternalScanner("just", JustExternalScanner{})
}
