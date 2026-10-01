//go:build grammar_subset && grammar_subset_ledger

package grammars

func init() {
	Register(LangEntry{
		Name:           "ledger",
		Extensions:     []string{".ledger", ".journal"},
		Language:       LedgerLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "[\n  (block_comment)\n  (comment)\n  (note)\n  (test)\n] @comment @spell\n\n[\n  (quantity)\n  (negative_quantity)\n] @number\n\n[\n  (date)\n  (effective_date)\n  (time)\n  (interval)\n] @string.special\n\n[\n  (option)\n  (option_value)\n  (check_in)\n  (check_out)\n] @markup.raw\n\n(account) @variable.member\n\n\"include\" @keyword.import\n\n[\n  \"account\"\n  \"alias\"\n  \"assert\"\n  \"check\"\n  \"commodity\"\n  \"comment\"\n  \"def\"\n  \"default\"\n  \"end\"\n  \"eval\"\n  \"format\"\n  \"nomarket\"\n  \"note\"\n  \"payee\"\n  \"test\"\n  \"A\"\n  \"Y\"\n  \"N\"\n  \"D\"\n  \"C\"\n  \"P\"\n] @keyword\n",
	})
}
