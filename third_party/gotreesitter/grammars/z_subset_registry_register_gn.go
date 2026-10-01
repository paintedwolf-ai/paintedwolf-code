//go:build grammar_subset && grammar_subset_gn

package grammars

func init() {
	Register(LangEntry{
		Name:           "gn",
		Extensions:     []string{".gn", ".gni"},
		Language:       GnLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "; Includes\n\n\"import\" @include\n\n; Conditionals\n\n[\n  \"if\"\n  \"else\"\n] @conditional\n\n; Repeats\n\n\"foreach\" @repeat\n\n; Operators\n\n[\n  \"=\"\n  \"+=\"\n  \"-=\"\n  \"!\"\n  \"+\"\n  \"-\"\n  \"<\"\n  \"<=\"\n  \">\"\n  \">=\"\n  \"==\"\n  \"!=\"\n  \"&&\"\n  \"||\"\n] @operator\n\n; Variables\n\n(identifier) @variable\n\n; Functions\n\n(call_expression function: (identifier) @function.call)\n\n; Fields\n\n(scope_access field: (identifier) @field)\n\n; Literals\n\n(string) @string\n\n(escape_sequence) @string.escape\n\n(expansion) @none\n\n(integer) @number\n\n(hex) @string.special\n\n(boolean) @boolean\n\n; Punctuation\n\n[ \"{\" \"}\" \"[\" \"]\" \"(\" \")\" ] @punctuation.bracket\n\n[\n  \".\"\n  \",\"\n] @punctuation.delimiter\n\n(expansion [\"$\" \"${\" \"}\"] @punctuation.special)\n\n; Comments\n\n(comment) @comment\n",
	})
}
