//go:build grammar_subset && grammar_subset_gitignore

package grammars

func init() {
	Register(LangEntry{
		Name:           "gitignore",
		Extensions:     []string{".gitignore"},
		Language:       GitignoreLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(comment) @comment @spell\n\n(pattern_char) @string.special.path\n\n[\n  (directory_separator)\n  (directory_separator_escaped)\n] @punctuation.delimiter\n\n[\n  (wildcard_char_single)\n  (wildcard_chars)\n  (wildcard_chars_allow_slash)\n] @character.special\n\n[\n  (pattern_char_escaped)\n  (bracket_char_escaped)\n] @string.escape\n\n(negation) @punctuation.special\n\n(bracket_negation) @operator\n\n; bracket expressions\n[\n  \"[\"\n  \"]\"\n] @punctuation.bracket\n\n(bracket_char) @constant\n\n(bracket_range\n  \"-\" @operator)\n\n(bracket_char_class) @constant.builtin\n",
	})
}
