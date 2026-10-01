//go:build grammar_subset && grammar_subset_vue

package grammars

func init() {
	Register(LangEntry{
		Name:           "vue",
		Extensions:     []string{".vue"},
		Language:       VueLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(tag_name) @tag\n(text) @string\n",
	})
}
