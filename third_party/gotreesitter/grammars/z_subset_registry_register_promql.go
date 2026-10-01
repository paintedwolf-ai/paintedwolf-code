//go:build grammar_subset && grammar_subset_promql

package grammars

func init() {
	Register(LangEntry{
		Name:           "promql",
		Extensions:     []string{".promql"},
		Language:       PromqlLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(metric_name) @function\n\n(label_name) @property\n(label_value) @string\n\n\"=\" @operator\n\n\"{\" @punctuation.bracket\n\"}\" @punctuation.bracket\n",
	})
}
