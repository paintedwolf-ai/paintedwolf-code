//go:build grammar_subset && grammar_subset_bitbake

package grammars

func init() {
	RegisterExternalScanner("bitbake", BitbakeExternalScanner{})
}
