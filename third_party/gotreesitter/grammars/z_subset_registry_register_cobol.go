//go:build grammar_subset && grammar_subset_cobol

package grammars

func init() {
	Register(LangEntry{
		Name:           "cobol",
		Extensions:     []string{".cob", ".cbl", ".cpy"},
		Language:       CobolLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(program_name) @type\n",
	})
}
