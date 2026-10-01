//go:build grammar_subset && grammar_subset_blade

package grammars

func init() {
	RegisterExternalScanner("blade", BladeExternalScanner{})
}
