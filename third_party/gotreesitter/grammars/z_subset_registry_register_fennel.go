//go:build grammar_subset && grammar_subset_fennel

package grammars

func init() {
	Register(LangEntry{
		Name:           "fennel",
		Extensions:     []string{".fnl"},
		Language:       FennelLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(symbol) @variable\n(number) @number\n",
	})
}
