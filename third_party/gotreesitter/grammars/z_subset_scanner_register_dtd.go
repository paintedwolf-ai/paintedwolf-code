//go:build grammar_subset && grammar_subset_dtd

package grammars

func init() {
	RegisterExternalScanner("dtd", DtdExternalScanner{})
}
