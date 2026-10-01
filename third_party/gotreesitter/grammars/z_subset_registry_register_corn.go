//go:build grammar_subset && grammar_subset_corn

package grammars

func init() {
	Register(LangEntry{
		Name:           "corn",
		Extensions:     []string{".corn"},
		Language:       CornLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "\"let\" @keyword\n\"in\" @keyword\n\n[\n  \"{\"\n  \"}\"\n  \"[\"\n  \"]\"\n] @punctuation.bracket\n\n(path_seg) @string.special.key\n\".\" @punctuation.delimiter\n\n(input) @constant\n(comment) @comment\n\n(string) @string\n(char) @string\n(integer) @number\n(float) @float\n(boolean) @boolean\n(null) @keyword\n\n(ERROR) @error\n",
	})
}
