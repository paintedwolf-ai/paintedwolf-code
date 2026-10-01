//go:build grammar_subset && grammar_subset_yuck

package grammars

func init() {
	RegisterExternalScanner("yuck", YuckExternalScanner{})
}
