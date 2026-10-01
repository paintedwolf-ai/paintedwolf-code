//go:build grammar_subset && grammar_subset_uxntal

package grammars

func init() {
	RegisterExternalScanner("uxntal", UxntalExternalScanner{})
}
