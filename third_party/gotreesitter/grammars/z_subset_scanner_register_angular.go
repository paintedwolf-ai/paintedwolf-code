//go:build grammar_subset && grammar_subset_angular

package grammars

func init() {
	RegisterExternalScanner("angular", AngularExternalScanner{})
}
