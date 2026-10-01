//go:build grammar_subset && grammar_subset_meson

package grammars

func init() {
	Register(LangEntry{
		Name:           "meson",
		Language:       MesonLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(comment) @comment\n(number) @number \n(bool) @boolean\n\n[\n  \"(\"\n  \")\"\n  \"{\"\n  \"}\"\n\t\"[\"\n\t\"]\"\n]\n@punctuation.bracket\n\n[\n  \"=\"\n\t\"==\"\n\t\"and\"\n\t\"+\"\n\t\"!=\"\n\t\"+=\"\n\t\"not\"\n] @operator\n\n[\n\"if\"\n\"elif\"\n\"else\"\n\"endif\"\n\n] @conditional\n[\n\"foreach\"\n\"endforeach\"\n(keyword_break)\n(keyword_continue)\n] @repeat\n\n;;; format\n(string) @string\n[\"@\"] @keyword\n\n(expression_statement\n\tobject: (identifier) @variable)\n(normal_command\n\tcommand: (identifier) @function)\n\n(pair\n\tkey: (identifier) @property)\n",
	})
}
