//go:build grammar_subset && grammar_subset_commonlisp

package grammars

func init() {
	Register(LangEntry{
		Name:           "commonlisp",
		Extensions:     []string{".cl", ".lisp", ".lsp"},
		Language:       CommonlispLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(sym_lit) @symbol\n",
	})
}
