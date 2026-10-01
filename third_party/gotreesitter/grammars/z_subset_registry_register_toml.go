//go:build grammar_subset && grammar_subset_toml

package grammars

func init() {
	Register(LangEntry{
		Name:           "toml",
		Extensions:     []string{".toml"},
		Language:       TomlLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "; Properties\n;-----------\n\n(bare_key) @property\n(quoted_key) @string\n\n; Literals\n;---------\n\n(boolean) @constant.builtin\n(comment) @comment\n(string) @string\n(integer) @number\n(float) @number\n(offset_date_time) @string.special\n(local_date_time) @string.special\n(local_date) @string.special\n(local_time) @string.special\n\n; Punctuation\n;------------\n\n\".\" @punctuation.delimiter\n\",\" @punctuation.delimiter\n\n\"=\" @operator\n\n\"[\" @punctuation.bracket\n\"]\" @punctuation.bracket\n\"[[\" @punctuation.bracket\n\"]]\" @punctuation.bracket\n\"{\" @punctuation.bracket\n\"}\" @punctuation.bracket\n",
	})
}
