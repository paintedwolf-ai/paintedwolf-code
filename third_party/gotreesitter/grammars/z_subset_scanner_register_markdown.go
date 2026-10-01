//go:build grammar_subset && grammar_subset_markdown

package grammars

func init() {
	RegisterExternalScanner("markdown", MarkdownExternalScanner{})
}
