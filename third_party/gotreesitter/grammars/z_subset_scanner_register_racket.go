//go:build grammar_subset && grammar_subset_racket

package grammars

func init() {
	RegisterExternalScanner("racket", RacketExternalScanner{})
}
