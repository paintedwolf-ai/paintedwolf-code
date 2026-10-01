//go:build grammar_subset && grammar_subset_jsdoc

package grammars

func init() {
	RegisterExternalScanner("jsdoc", JsdocExternalScanner{})
}
