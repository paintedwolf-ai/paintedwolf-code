//go:build grammar_subset && grammar_subset_beancount

package grammars

func init() {
	Register(LangEntry{
		Name:           "beancount",
		Extensions:     []string{".beancount"},
		Language:       BeancountLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(date) @variable.member\n\n(txn) @attribute\n\n(account) @type\n\n(amount) @number\n\n(incomplete_amount) @number\n\n(compound_amount) @number\n\n(amount_tolerance) @number\n\n(currency) @property\n\n(key) @label\n\n(string) @string\n\n(narration) @string @spell\n\n(payee) @string @spell\n\n(tag) @constant\n\n(link) @constant\n\n[\n  (minus)\n  (plus)\n  (slash)\n  (asterisk)\n] @operator\n\n(comment) @comment @spell\n",
	})
}
