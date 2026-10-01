//go:build grammar_subset && grammar_subset_typst

package grammars

func init() {
	RegisterExternalScanner("typst", TypstExternalScanner{})
}
