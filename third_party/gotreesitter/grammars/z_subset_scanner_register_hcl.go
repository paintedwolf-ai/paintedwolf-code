//go:build grammar_subset && grammar_subset_hcl

package grammars

func init() {
	RegisterExternalScanner("hcl", HclExternalScanner{})
}
