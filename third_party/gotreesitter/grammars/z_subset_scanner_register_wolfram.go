//go:build grammar_subset && grammar_subset_wolfram

package grammars

func init() {
	RegisterExternalScanner("wolfram", WolframExternalScanner{})
}
