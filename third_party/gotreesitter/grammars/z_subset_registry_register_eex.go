//go:build grammar_subset && grammar_subset_eex

package grammars

func init() {
	Register(LangEntry{
		Name:           "eex",
		Extensions:     []string{".eex"},
		Language:       EexLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "; wrapping in (directive .. ) prevents us from highlighting '%>' in a comment as a keyword\n(directive [\"<%\" \"<%=\" \"<%%\" \"<%%=\" \"%>\"] @keyword)\n\n(comment) @comment\n",
	})
}
