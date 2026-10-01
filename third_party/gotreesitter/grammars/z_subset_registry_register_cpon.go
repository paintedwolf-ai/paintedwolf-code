//go:build grammar_subset && grammar_subset_cpon

package grammars

func init() {
	Register(LangEntry{
		Name:           "cpon",
		Extensions:     []string{".cpon"},
		Language:       CponLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "; Literals\n\n(string) @string\n(escape_sequence) @string.escape\n\n(hex_blob\n  \"x\" @character.special\n  (_) @string)\n\n(esc_blob\n  \"b\" @character.special\n  (_) @string)\n\n(datetime\n  \"d\" @character.special\n  (_) @string.special)\n\n(_ key: (_) @label)\n\n(number) @number\n\n(float) @float\n\n(boolean) @boolean\n\n(null) @constant.builtin\n\n; Punctuation\n\n[\n  \",\"\n  \":\"\n] @punctuation.delimiter\n\n[ \"{\" \"}\" ] @punctuation.bracket\n\n[ \"[\" \"]\" ] @punctuation.bracket\n\n[ \"<\" \">\" ] @punctuation.bracket\n\n((\"\\\"\" @conceal)\n (#set! conceal \"\"))\n\n; Comments\n\n(comment) @comment @spell\n\n; Errors\n\n(ERROR) @error\n",
	})
}
