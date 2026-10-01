//go:build grammar_subset && grammar_subset_json5

package grammars

func init() {
	Register(LangEntry{
		Name:           "json5",
		Extensions:     []string{".json5"},
		Language:       Json5Language,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(string) @string\n\n(identifier) @constant\n\n(number) @constant.numeric\n\n(null) @constant.builtin\n\n[(true) (false)] @constant.builtin.boolean\n\n(comment) @comment\n",
	})
}
