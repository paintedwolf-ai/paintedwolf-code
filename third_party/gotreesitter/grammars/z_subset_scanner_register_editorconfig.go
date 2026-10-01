//go:build grammar_subset && grammar_subset_editorconfig

package grammars

func init() {
	RegisterExternalScanner("editorconfig", EditorconfigExternalScanner{})
}
