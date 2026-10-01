//go:build grammar_subset && grammar_subset_c_sharp

package grammars

func init() {
	RegisterExternalScanner("c_sharp", CSharpExternalScanner{})
}
