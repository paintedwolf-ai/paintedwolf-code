//go:build grammar_subset && grammar_subset_scss

package grammars

func init() {
	RegisterExternalScanner("scss", ScssExternalScanner{})
	RegisterExternalLexStates("scss", [][]bool{
		{false, false, false, false},
		{true, true, true, true},
		{false, true, false, false},
		{true, true, false, true},
		{true, true, false, false},
		{false, false, false, true},
	})
}
