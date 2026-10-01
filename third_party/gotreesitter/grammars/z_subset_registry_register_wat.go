//go:build grammar_subset && grammar_subset_wat

package grammars

func init() {
	Register(LangEntry{
		Name:           "wat",
		Extensions:     []string{".wat", ".wast"},
		Language:       WatLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(identifier) @variable\n(op_nullary) @function\n",
	})
}
