//go:build grammar_subset && grammar_subset_svelte

package grammars

func init() {
	RegisterExternalScanner("svelte", SvelteExternalScanner{})
}
