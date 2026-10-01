//go:build grammar_subset && grammar_subset_ini

package grammars

func init() {
	Register(LangEntry{
		Name:           "ini",
		Extensions:     []string{".ini", ".cfg", ".conf"},
		Language:       IniLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(section_name\n  (text) @type) ; consistency with toml\n\n(comment) @comment @spell\n\n[\n  \"[\"\n  \"]\"\n] @punctuation.bracket\n\n\"=\" @operator\n\n(setting\n  (setting_name) @property)\n\n; (setting_value) @none ; grammar does not support subtypes\n",
	})
}
