//go:build grammar_subset && grammar_subset_doxygen

package grammars

func init() {
	RegisterExternalScanner("doxygen", DoxygenExternalScanner{})
}
