//go:build grammar_subset && grammar_subset_apex

package grammars

func init() {
	Register(LangEntry{
		Name:           "apex",
		Extensions:     []string{".cls", ".trigger"},
		Language:       ApexLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(modifier) @keyword\n(class_declaration (identifier) @type)\n(method_declaration (identifier) @function)\n",
	})
}
