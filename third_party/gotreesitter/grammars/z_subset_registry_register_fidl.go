//go:build grammar_subset && grammar_subset_fidl

package grammars

func init() {
	Register(LangEntry{
		Name:           "fidl",
		Extensions:     []string{".fidl"},
		Language:       FidlLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "[\n  \"ajar\"\n  \"alias\"\n  \"as\"\n  \"bits\"\n  \"closed\"\n  \"compose\"\n  \"const\"\n  \"enum\"\n  \"error\"\n  \"flexible\"\n  \"library\"\n  \"open\"\n  ; \"optional\" we did not specify a node for optional yet\n  \"overlay\"\n  \"protocol\"\n  \"reserved\"\n  \"resource\"\n  \"service\"\n  \"strict\"\n  \"struct\"\n  \"table\"\n  \"type\"\n  \"union\"\n  \"using\"\n] @keyword\n\n(primitives_type) @type.builtin\n\n(builtin_complex_type) @type.builtin\n\n(const_declaration\n  (identifier) @constant)\n\n[\n  \"=\"\n  \"|\"\n  \"&\"\n  \"->\"\n] @operator\n\n(attribute\n  \"@\" @attribute\n  (identifier) @attribute)\n\n(string_literal) @string\n\n(numeric_literal) @number\n\n[\n  (true)\n  (false)\n] @boolean\n\n(comment) @comment\n\n[\n  \"(\"\n  \")\"\n  \"<\"\n  \">\"\n  \"{\"\n  \"}\"\n] @punctuation.bracket\n",
	})
}
