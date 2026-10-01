//go:build grammar_subset && grammar_subset_cooklang

package grammars

func init() {
	Register(LangEntry{
		Name:           "cooklang",
		Extensions:     []string{".cook"},
		Language:       CooklangLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(metadata) @comment\n\n(comment) @comment @spell\n\n[\n  \"{\"\n  \"}\"\n] @punctuation.bracket\n\n\"%\" @punctuation.special\n\n(ingredient\n  \"@\" @punctuation.delimiter\n  (name)? @string.special.symbol\n  (amount\n    (quantity)? @number\n    (units)? @constant)?)\n\n(timer\n  \"~\" @punctuation.delimiter\n  (name)? @string.special.symbol\n  (amount\n    (quantity)? @number\n    (units)? @constant)?)\n\n(cookware\n  \"#\" @punctuation.delimiter\n  (name)? @string.special.symbol\n  (amount\n    (quantity)? @number\n    (units)? @constant)?)\n",
	})
}
