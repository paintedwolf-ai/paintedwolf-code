//go:build grammar_subset && grammar_subset_jinja2

package grammars

func init() {
	Register(LangEntry{
		Name:           "jinja2",
		Extensions:     []string{".j2", ".jinja2", ".jinja"},
		Language:       Jinja2Language,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(jinja_expression) @keyword\n",
	})
}
