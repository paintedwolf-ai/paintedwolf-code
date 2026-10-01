//go:build grammar_subset && grammar_subset_todotxt

package grammars

func init() {
	Register(LangEntry{
		Name:           "todotxt",
		Language:       TodotxtLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(done_task) @comment\n(task (priority) @keyword)\n(task (date) @comment)\n(task (kv) @comment)\n(task (project) @string)\n(task (context) @type)\n",
	})
}
