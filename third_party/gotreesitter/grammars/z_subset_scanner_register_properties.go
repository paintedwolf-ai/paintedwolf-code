//go:build grammar_subset && grammar_subset_properties

package grammars

func init() {
	RegisterExternalScanner("properties", PropertiesExternalScanner{})
}
