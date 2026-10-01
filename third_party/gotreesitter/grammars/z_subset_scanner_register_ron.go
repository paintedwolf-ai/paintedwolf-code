//go:build grammar_subset && grammar_subset_ron

package grammars

func init() {
	RegisterExternalScanner("ron", RonExternalScanner{})
}
