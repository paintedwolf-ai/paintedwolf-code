//go:build grammar_subset && grammar_subset_javascript

package grammars

func init() {
	RegisterExternalScanner("javascript", JavaScriptExternalScanner{})
}
