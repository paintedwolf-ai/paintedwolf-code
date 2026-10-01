//go:build grammar_subset && grammar_subset_elsa

package grammars

func init() {
	Register(LangEntry{
		Name:           "elsa",
		Extensions:     []string{".elsa"},
		Language:       ElsaLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "; Keywords\n\n[\n  \"eval\"\n  \"let\"\n] @keyword\n\n; Function\n\n(function) @function\n\n; Method\n\n(method) @method\n\n; Parameter\n\n(parameter) @parameter\n\n; Variables\n\n(identifier) @variable\n\n; Operators\n\n[\n  \"\\\\\"\n  \"->\"\n  \"=\"\n  (step)\n] @operator\n\n; Punctuation\n\n[\"(\" \")\"] @punctuation.bracket\n\n\":\" @punctuation.delimiter\n\n; Comments\n\n(comment) @comment\n",
	})
}
