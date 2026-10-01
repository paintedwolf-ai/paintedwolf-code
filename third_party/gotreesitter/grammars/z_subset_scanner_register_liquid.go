//go:build grammar_subset && grammar_subset_liquid

package grammars

func init() {
	RegisterExternalScanner("liquid", LiquidExternalScanner{})
}
