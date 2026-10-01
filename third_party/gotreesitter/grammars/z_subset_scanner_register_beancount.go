//go:build grammar_subset && grammar_subset_beancount

package grammars

func init() {
	RegisterExternalScanner("beancount", BeancountExternalScanner{})
}
