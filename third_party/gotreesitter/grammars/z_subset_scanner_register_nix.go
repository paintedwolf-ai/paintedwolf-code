//go:build grammar_subset && grammar_subset_nix

package grammars

func init() {
	RegisterExternalScanner("nix", NixExternalScanner{})
}
