//go:build grammar_subset && grammar_subset_awk

package grammars

func init() {
	RegisterExternalScanner("awk", AwkExternalScanner{})
}
