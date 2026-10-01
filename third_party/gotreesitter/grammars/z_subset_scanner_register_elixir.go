//go:build grammar_subset && grammar_subset_elixir

package grammars

func init() {
	RegisterExternalScanner("elixir", ElixirExternalScanner{})
}
