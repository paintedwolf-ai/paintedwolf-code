//go:build grammar_subset && grammar_subset_cmake

package grammars

func init() {
	RegisterExternalScanner("cmake", CmakeExternalScanner{})
}
