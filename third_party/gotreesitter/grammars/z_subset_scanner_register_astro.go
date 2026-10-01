//go:build grammar_subset && grammar_subset_astro

package grammars

func init() {
	RegisterExternalScanner("astro", AstroExternalScanner{})
}
