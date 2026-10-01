//go:build grammar_subset && grammar_subset_tcl

package grammars

func init() {
	RegisterExternalScanner("tcl", TclExternalScanner{})
}
