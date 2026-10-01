//go:build grammar_subset && grammar_subset_embedded_template

package grammars

func init() {
	Register(LangEntry{
		Name:           "embedded_template",
		Extensions:     []string{".erb", ".ejs"},
		Language:       EmbeddedTemplateLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(comment_directive) @comment\n\n[\n  \"<%#\"\n  \"<%\"\n  \"<%=\"\n  \"<%_\"\n  \"<%-\"\n  \"%>\"\n  \"-%>\"\n  \"_%>\"\n] @keyword\n",
	})
}
