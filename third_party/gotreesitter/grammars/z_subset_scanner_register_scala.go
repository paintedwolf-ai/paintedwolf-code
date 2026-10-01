//go:build grammar_subset && grammar_subset_scala

package grammars

func init() {
	RegisterExternalScanner("scala", ScalaExternalScanner{})
}
