//go:build grammar_subset && grammar_subset_rust

package grammars

func init() {
	RegisterExternalScanner("rust", RustExternalScanner{})
}
