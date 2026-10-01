//go:build grammar_subset && grammar_subset_chatito

package grammars

func init() {
	Register(LangEntry{
		Name:           "chatito",
		Extensions:     []string{".chatito"},
		Language:       ChatitoLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "; Punctuation\n[\n  \"%[\"\n  \"@[\"\n  \"~[\"\n  \"*[\"\n  \"]\"\n  \"(\"\n  \")\"\n] @punctuation.bracket\n\n[\n  eq: _\n  \",\"\n] @punctuation.delimiter\n\n[\n  \"%\"\n  \"?\"\n  \"#\"\n] @punctuation.special\n\n; Entities\n(intent) @module\n\n(slot) @type\n\n(variation) @variable.member\n\n(alias) @embedded\n\n(number) @number\n\n(argument\n  key: (string) @attribute\n  value: (string) @string)\n\n(escape) @string.escape\n\n; Import\n\"import\" @keyword\n\n(file) @string.special\n\n; Text\n(word) @markup\n\n; Comment\n(comment) @comment\n",
	})
}
