//go:build grammar_subset && grammar_subset_gomod

package grammars

func init() {
	Register(LangEntry{
		Name:           "gomod",
		Language:       GomodLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "[\n  \"require\"\n  \"replace\"\n  \"go\"\n  \"toolchain\"\n  \"exclude\"\n  \"retract\"\n  \"module\"\n] @keyword\n\n\"=>\" @operator\n\n(comment) @comment\n\n[\n(version)\n(go_version)\n] @string\n",
	})
}
