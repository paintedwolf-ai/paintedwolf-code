//go:build grammar_subset && grammar_subset_squirrel

package grammars

func init() {
	RegisterExternalScanner("squirrel", SquirrelExternalScanner{})
}
