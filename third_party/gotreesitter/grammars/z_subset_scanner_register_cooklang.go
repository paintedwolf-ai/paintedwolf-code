//go:build grammar_subset && grammar_subset_cooklang

package grammars

func init() {
	RegisterExternalScanner("cooklang", CooklangExternalScanner{})
}
