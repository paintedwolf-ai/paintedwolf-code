//go:build grammar_subset && grammar_subset_move

package grammars

func init() {
	RegisterExternalScanner("move", MoveExternalScanner{})
}
