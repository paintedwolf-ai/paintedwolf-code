//go:build grammar_subset && grammar_subset_rst

package grammars

func init() {
	Register(LangEntry{
		Name:           "rst",
		Extensions:     []string{".rst"},
		Language:       RstLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(title) @markup.heading\n",
	})
}
