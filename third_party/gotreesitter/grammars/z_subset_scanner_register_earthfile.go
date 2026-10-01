//go:build grammar_subset && grammar_subset_earthfile

package grammars

func init() {
	RegisterExternalScanner("earthfile", EarthfileExternalScanner{})
}
