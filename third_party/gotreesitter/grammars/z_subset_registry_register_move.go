//go:build grammar_subset && grammar_subset_move

package grammars

func init() {
	Register(LangEntry{
		Name:           "move",
		Extensions:     []string{".move"},
		Language:       MoveLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "\"module\" @keyword\n(num_literal) @number\n(address_literal) @number\n(line_comment) @comment\n(block_comment) @comment\n(identifier) @type\n",
	})
}
