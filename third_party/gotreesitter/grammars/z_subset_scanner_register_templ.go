//go:build grammar_subset && grammar_subset_templ

package grammars

func init() {
	RegisterExternalScanner("templ", TemplExternalScanner{})
}
