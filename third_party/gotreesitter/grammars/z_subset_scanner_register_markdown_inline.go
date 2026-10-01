//go:build grammar_subset && grammar_subset_markdown_inline

package grammars

func init() {
	RegisterExternalScanner("markdown_inline", MarkdownInlineExternalScanner{})
}
