//go:build grammar_subset && grammar_subset_typst

package grammars

func init() {
	Register(LangEntry{
		Name:           "typst",
		Extensions:     []string{".typ"},
		Language:       TypstLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(text) @string\n",
	})
}
