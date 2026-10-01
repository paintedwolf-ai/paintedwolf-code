//go:build grammar_subset && grammar_subset_angular

package grammars

func init() {
	Register(LangEntry{
		Name:           "angular",
		Language:       AngularLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(tag_name) @tag\n(text) @string\n",
	})
}
