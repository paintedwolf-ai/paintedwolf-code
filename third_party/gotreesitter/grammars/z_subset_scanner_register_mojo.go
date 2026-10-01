//go:build grammar_subset && grammar_subset_mojo

package grammars

func init() {
	RegisterExternalScanner("mojo", MojoExternalScanner{})
}
