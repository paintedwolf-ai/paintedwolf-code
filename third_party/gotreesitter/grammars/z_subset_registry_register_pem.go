//go:build grammar_subset && grammar_subset_pem

package grammars

func init() {
	Register(LangEntry{
		Name:           "pem",
		Extensions:     []string{".pem"},
		Language:       PemLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "[ \"BEGIN\" \"END\" ] @keyword\n\n(dashes) @punctuation.delimiter\n\n(label) @type\n\n(data) @markup\n\n(comment) @comment\n",
	})
}
