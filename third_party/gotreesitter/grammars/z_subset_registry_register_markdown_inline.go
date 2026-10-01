//go:build grammar_subset && grammar_subset_markdown_inline

package grammars

func init() {
	Register(LangEntry{
		Name:           "markdown_inline",
		Language:       MarkdownInlineLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "((emphasis\n  (strong_emphasis)) @markup.strong)\n\n((strong_emphasis) @markup.strong\n  (#not-has-parent? @markup.strong emphasis))\n",
	})
}
