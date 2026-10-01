//go:build grammar_subset && grammar_subset_typescript

package grammars

func init() {
	Register(LangEntry{
		Name:           "typescript",
		Extensions:     []string{".ts"},
		Language:       TypescriptLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "; Types\n\n(type_identifier) @type\n(predefined_type) @type.builtin\n\n((identifier) @type\n (#match? @type \"^[A-Z]\"))\n\n(type_arguments\n  \"<\" @punctuation.bracket\n  \">\" @punctuation.bracket)\n\n; Variables\n\n(required_parameter (identifier) @variable.parameter)\n(optional_parameter (identifier) @variable.parameter)\n\n; Keywords\n\n[ \"abstract\"\n  \"declare\"\n  \"enum\"\n  \"export\"\n  \"implements\"\n  \"interface\"\n  \"keyof\"\n  \"namespace\"\n  \"private\"\n  \"protected\"\n  \"public\"\n  \"type\"\n  \"readonly\"\n  \"override\"\n  \"satisfies\"\n] @keyword\n",
	})
}
