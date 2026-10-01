//go:build grammar_subset && grammar_subset_hlsl

package grammars

func init() {
	RegisterExternalScanner("hlsl", HlslExternalScanner{})
}
