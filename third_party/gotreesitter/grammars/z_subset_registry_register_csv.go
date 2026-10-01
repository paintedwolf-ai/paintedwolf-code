//go:build grammar_subset && grammar_subset_csv

package grammars

func init() {
	Register(LangEntry{
		Name:           "csv",
		Extensions:     []string{".csv", ".tsv"},
		Language:       CsvLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(text) @string\n(number) @number\n(float) @float\n(boolean) @boolean\n\",\" @punctuation.delimiter\n",
	})
}
