//go:build grammar_subset && grammar_subset_wgsl

package grammars

func init() {
	RegisterExternalScanner("wgsl", WgslExternalScanner{})
}
