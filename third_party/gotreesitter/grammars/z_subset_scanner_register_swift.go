//go:build grammar_subset && grammar_subset_swift

package grammars

func init() {
	RegisterExternalScanner("swift", SwiftExternalScanner{})
}
