//go:build grammar_subset && grammar_subset_blade

package grammars

func init() {
	Register(LangEntry{
		Name:           "blade",
		Extensions:     []string{".blade.php"},
		Language:       BladeLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "; inherits: html\n\n[\n  (directive)\n  (directive_start)\n  (directive_end)\n] @tag\n\n[\n  (php_tag)\n  (php_end_tag)\n  \"{{\"\n  \"}}\"\n  \"{!!\"\n  \"!!}\"\n  \"(\"\n  \")\"\n] @punctuation.bracket\n",
	})
}
