//go:build grammar_subset && grammar_subset_bibtex

package grammars

func init() {
	Register(LangEntry{
		Name:           "bibtex",
		Extensions:     []string{".bib"},
		Language:       BibtexLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "[\n  (string_type)\n  (preamble_type)\n  (entry_type)\n] @keyword\n\n[\n  (junk)\n  (comment)\n] @comment\n\n[\n  \"=\"\n  \"#\"\n] @operator\n\n(command) @function.builtin\n\n(number) @number\n\n(field\n  name: (identifier) @variable.builtin)\n\n(token\n  (identifier) @variable.parameter)\n\n[\n  (brace_word)\n  (quote_word)\n] @string\n\n[\n  (key_brace)\n  (key_paren)\n] @attribute\n\n(string\n  name: (identifier) @constant)\n\n[\n  \"{\"\n  \"}\"\n  \"(\"\n  \")\"\n] @punctuation.bracket\n\n\",\" @punctuation.delimiter\n",
	})
}
