//go:build grammar_subset && grammar_subset_cairo

package grammars

func init() {
	RegisterExternalScanner("cairo", CairoExternalScanner{})
}
