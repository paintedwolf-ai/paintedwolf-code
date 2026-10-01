//go:build grammar_subset && grammar_subset_hlsl

package grammars

func init() {
	Register(LangEntry{
		Name:           "hlsl",
		Extensions:     []string{".hlsl", ".fx"},
		Language:       HlslLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(identifier) @variable\n",
	})
}
