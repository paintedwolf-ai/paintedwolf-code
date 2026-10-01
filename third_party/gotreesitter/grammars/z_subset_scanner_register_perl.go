//go:build grammar_subset && grammar_subset_perl

package grammars

func init() {
	RegisterExternalScanner("perl", PerlExternalScanner{})
}
