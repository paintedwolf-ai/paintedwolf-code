//go:build grammar_subset && grammar_subset_v

package grammars

func init() {
	Register(LangEntry{
		Name:           "v",
		Extensions:     []string{".v", ".vsh"},
		Language:       VLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(function_declaration (identifier) @function)\n",
	})
}
