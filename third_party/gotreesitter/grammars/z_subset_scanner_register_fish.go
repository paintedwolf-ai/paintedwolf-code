//go:build grammar_subset && grammar_subset_fish

package grammars

func init() {
	RegisterExternalScanner("fish", FishExternalScanner{})
}
