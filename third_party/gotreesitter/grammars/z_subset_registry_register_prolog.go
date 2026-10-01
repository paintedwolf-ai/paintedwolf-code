//go:build grammar_subset && grammar_subset_prolog

package grammars

func init() {
	Register(LangEntry{
		Name:           "prolog",
		Extensions:     []string{".pl", ".pro"},
		Language:       PrologLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(atom) @function\n",
	})
}
