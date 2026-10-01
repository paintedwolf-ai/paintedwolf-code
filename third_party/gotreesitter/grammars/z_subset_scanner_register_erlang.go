//go:build grammar_subset && grammar_subset_erlang

package grammars

func init() {
	RegisterExternalScanner("erlang", ErlangExternalScanner{})
}
