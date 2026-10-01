//go:build grammar_subset && grammar_subset_gdscript

package grammars

func init() {
	RegisterExternalScanner("gdscript", GdscriptExternalScanner{})
}
