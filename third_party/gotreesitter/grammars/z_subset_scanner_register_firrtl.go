//go:build grammar_subset && grammar_subset_firrtl

package grammars

func init() {
	RegisterExternalScanner("firrtl", FirrtlExternalScanner{})
}
