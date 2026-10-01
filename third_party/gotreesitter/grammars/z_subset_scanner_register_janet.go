//go:build grammar_subset && grammar_subset_janet

package grammars

func init() {
	RegisterExternalScanner("janet", JanetExternalScanner{})
}
