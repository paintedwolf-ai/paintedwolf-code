//go:build grammar_subset && grammar_subset_hack

package grammars

func init() {
	Register(LangEntry{
		Name:           "hack",
		Extensions:     []string{".hack", ".hh"},
		Language:       HackLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(comment) @comment\n\n(string) @string\n(heredoc) @string\n(prefixed_string) @string\n\n[\n  \"class\"\n  \"interface\"\n  \"trait\"\n  \"public\"\n  \"protected\"\n  \"private\"\n  \"static\"\n  \"async\"\n  \"function\"\n  \"return\"\n  \"if\"\n  \"else\"\n  \"elseif\"\n  \"while\"\n  \"for\"\n  \"foreach\"\n  \"break\"\n  \"continue\"\n  \"type\"\n  \"new\"\n  \"throw\"\n] @keyword\n\n(type_specifier) @type\n",
	})
}
