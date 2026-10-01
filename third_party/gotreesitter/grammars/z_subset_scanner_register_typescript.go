//go:build grammar_subset && grammar_subset_typescript

package grammars

func init() {
	RegisterExternalScanner("typescript", TypeScriptExternalScanner{})
}
