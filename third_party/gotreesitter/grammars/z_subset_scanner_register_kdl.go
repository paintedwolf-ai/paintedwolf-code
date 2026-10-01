//go:build grammar_subset && grammar_subset_kdl

package grammars

func init() {
	RegisterExternalScanner("kdl", KdlExternalScanner{})
}
