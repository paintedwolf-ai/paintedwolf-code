//go:build grammar_subset && grammar_subset_java

package grammars

func init() {
	registerTokenSourceFactory("java", NewJavaTokenSourceOrEOF)
}
