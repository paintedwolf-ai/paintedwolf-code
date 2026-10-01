//go:build grammar_subset && grammar_subset_haskell

package grammars

func init() {
	RegisterExternalScanner("haskell", HaskellExternalScanner{})
}
