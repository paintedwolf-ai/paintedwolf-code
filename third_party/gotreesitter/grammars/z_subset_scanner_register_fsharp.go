//go:build grammar_subset && grammar_subset_fsharp

package grammars

func init() {
	RegisterExternalScanner("fsharp", FsharpExternalScanner{})
}
