//go:build grammar_subset && grammar_subset_json

package grammars

func init() {
	Register(LangEntry{
		Name:           "json",
		Extensions:     []string{".json"},
		Language:       JsonLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(string) @string\n\n(pair\n  key: (_) @string.special.key)\n\n(number) @number\n\n[\n  (null)\n  (true)\n  (false)\n] @constant.builtin\n\n(escape_sequence) @escape\n\n(comment) @comment\n",
	})
}
