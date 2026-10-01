//go:build grammar_subset && grammar_subset_cpp

package grammars

func init() {
	RegisterExternalScanner("cpp", CppExternalScanner{})
}
