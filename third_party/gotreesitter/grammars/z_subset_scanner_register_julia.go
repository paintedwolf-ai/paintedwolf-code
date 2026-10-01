//go:build grammar_subset && grammar_subset_julia

package grammars

func init() {
	RegisterExternalScanner("julia", JuliaExternalScanner{})
}
