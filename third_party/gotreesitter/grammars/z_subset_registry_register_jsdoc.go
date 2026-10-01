//go:build grammar_subset && grammar_subset_jsdoc

package grammars

func init() {
	Register(LangEntry{
		Name:           "jsdoc",
		Language:       JsdocLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(tag_name) @keyword\n(type) @type\n",
	})
}
