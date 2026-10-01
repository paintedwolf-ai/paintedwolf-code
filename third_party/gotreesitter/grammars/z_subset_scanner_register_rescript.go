//go:build grammar_subset && grammar_subset_rescript

package grammars

func init() {
	RegisterExternalScanner("rescript", RescriptExternalScanner{})
}
