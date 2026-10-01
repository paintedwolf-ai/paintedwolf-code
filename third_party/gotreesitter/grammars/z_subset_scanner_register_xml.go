//go:build grammar_subset && grammar_subset_xml

package grammars

func init() {
	RegisterExternalScanner("xml", XMLExternalScanner{})
}
