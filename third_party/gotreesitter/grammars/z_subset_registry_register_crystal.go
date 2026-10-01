//go:build grammar_subset && grammar_subset_crystal

package grammars

func init() {
	Register(LangEntry{
		Name:           "crystal",
		Extensions:     []string{".cr"},
		Language:       CrystalLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(identifier) @variable\n(integer) @number\n",
	})
}
