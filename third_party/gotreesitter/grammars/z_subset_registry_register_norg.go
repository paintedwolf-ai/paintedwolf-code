//go:build grammar_subset && grammar_subset_norg

package grammars

func init() {
	Register(LangEntry{
		Name:           "norg",
		Extensions:     []string{".norg"},
		Language:       NorgLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(heading1_prefix) @punctuation.special\n\n(heading1\n  (paragraph_segment) @markup.heading)\n\n(paragraph\n  (paragraph_segment) @markup)\n",
	})
}
