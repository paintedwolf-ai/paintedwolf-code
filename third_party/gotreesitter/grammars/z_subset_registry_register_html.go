//go:build grammar_subset && grammar_subset_html

package grammars

func init() {
	Register(LangEntry{
		Name:           "html",
		Extensions:     []string{".html", ".htm"},
		Language:       HtmlLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(tag_name) @tag\n(erroneous_end_tag_name) @tag.error\n(doctype) @constant\n(attribute_name) @attribute\n(attribute_value) @string\n(comment) @comment\n\n[\n  \"<\"\n  \">\"\n  \"</\"\n  \"/>\"\n] @punctuation.bracket\n",
	})
}
