//go:build grammar_subset && grammar_subset_eds

package grammars

func init() {
	Register(LangEntry{
		Name:           "eds",
		Extensions:     []string{".eds"},
		Language:       EdsLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(section_name) @type\n(key) @property\n(value) @string\n",
	})
}
