//go:build grammar_subset && grammar_subset_nim

package grammars

func init() {
	RegisterExternalScanner("nim", NimExternalScanner{})
}
