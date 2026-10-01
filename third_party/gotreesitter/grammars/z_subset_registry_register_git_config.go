//go:build grammar_subset && grammar_subset_git_config

package grammars

func init() {
	Register(LangEntry{
		Name:           "git_config",
		Extensions:     []string{".gitconfig"},
		Language:       GitConfigLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(section_name) @tag\n\n((section_name) @function.builtin\n (#eq? @function.builtin \"include\"))\n\n((section_header\n   (section_name) @function.builtin\n   (subsection_name))\n (#eq? @function.builtin \"includeIf\"))\n\n(variable (name) @property)\n[(true) (false)] @constant.builtin\n(integer) @number\n\n[(string) (subsection_name)] @string\n\n((string) @string.special.path\n (#match? @string.special.path \"^(~|./|/)\"))\n\n[\n  \"[\"\n  \"]\"\n  \"\\\"\"\n] @punctuation.bracket\n\n\"=\" @punctuation.delimiter\n\n(comment) @comment\n",
	})
}
