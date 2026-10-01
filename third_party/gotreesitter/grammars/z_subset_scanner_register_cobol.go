//go:build grammar_subset && grammar_subset_cobol

package grammars

func init() {
	RegisterExternalScanner("cobol", CobolExternalScanner{})
}
