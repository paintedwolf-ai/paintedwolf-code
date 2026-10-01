//go:build grammar_subset && grammar_subset_tmux

package grammars

func init() {
	Register(LangEntry{
		Name:           "tmux",
		Language:       TmuxLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(command) @keyword\n(command_line_option) @attribute\n(option) @property\n(value) @string\n",
	})
}
