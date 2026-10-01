//go:build grammar_subset && grammar_subset_elm

package grammars

func init() {
	RegisterExternalScanner("elm", ElmExternalScanner{})
	RegisterExternalLexStates("elm", elmExternalLexStates)
}
