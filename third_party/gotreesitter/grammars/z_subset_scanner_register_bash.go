//go:build grammar_subset && grammar_subset_bash

package grammars

func init() {
	RegisterExternalScanner("bash", BashExternalScanner{})
	RegisterExternalLexStates("bash", bashExternalLexStates)
}
