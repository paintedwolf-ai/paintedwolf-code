//go:build grammar_subset && grammar_subset_wolfram

package grammars

func init() {
	Register(LangEntry{
		Name:           "wolfram",
		Extensions:     []string{".wl", ".m", ".nb"},
		Language:       WolframLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(symbol) @symbol\n",
	})
}
