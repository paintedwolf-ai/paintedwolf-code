//go:build grammar_subset && grammar_subset_ruby

package grammars

func init() {
	RegisterExternalScanner("ruby", RubyExternalScanner{})
}
