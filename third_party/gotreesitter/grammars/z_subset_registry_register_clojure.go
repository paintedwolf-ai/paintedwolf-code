//go:build grammar_subset && grammar_subset_clojure

package grammars

func init() {
	Register(LangEntry{
		Name:           "clojure",
		Extensions:     []string{".clj", ".cljs", ".cljc", ".edn"},
		Language:       ClojureLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: ";; Literals\n\n(num_lit) @number\n\n[\n  (char_lit)\n  (str_lit)\n] @string\n\n[\n (bool_lit)\n (nil_lit)\n] @constant.builtin\n\n(kwd_lit) @constant\n\n;; Comments\n\n(comment) @comment\n\n;; Treat quasiquotation as operators for the purpose of highlighting.\n\n[\n \"'\"\n \"`\"\n \"~\"\n \"@\"\n \"~@\"\n] @operator\n",
	})
}
