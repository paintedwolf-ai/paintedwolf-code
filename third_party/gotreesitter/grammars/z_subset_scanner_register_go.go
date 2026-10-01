//go:build grammar_subset && grammar_subset_go

package grammars

func init() {
	RegisterExternalScanner("go", GoExternalScanner{})
}
