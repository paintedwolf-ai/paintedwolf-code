//go:build grammar_subset && grammar_subset_requirements

package grammars

func init() {
	Register(LangEntry{
		Name:           "requirements",
		Language:       RequirementsLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: ";; packages\n\n(package) @variable\n\n(extras (package) @variable.parameter)\n\n(path) @markup.link\n\n(url) @markup.link.url\n\n;; versions\n\n(version_cmp) @operator\n\n(version) @number\n\n;; markers\n\n(marker_var) @attribute\n\n(marker_op) @keyword\n\n;; options\n\n(option) @function\n\n\"=\" @operator\n\n;; punctuation\n\n[ \"[\" \"]\" \"(\" \")\" ] @punctuation.bracket\n\n[ \",\" \";\" \"@\" ] @punctuation.delimiter\n\n[ \"${\" \"}\" ] @punctuation.special\n\n;; misc\n\n(env_var) @constant\n\n(quoted_string) @string\n\n(linebreak) @escape\n\n(comment) @comment\n",
	})
}
