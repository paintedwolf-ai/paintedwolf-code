//go:build grammar_subset && grammar_subset_janet

package grammars

func init() {
	Register(LangEntry{
		Name:           "janet",
		Extensions:     []string{".janet"},
		Language:       JanetLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(num_lit) @number\n\n[\n (buf_lit)\n (long_buf_lit)\n (long_str_lit)\n (str_lit)\n] @string\n\n[\n (bool_lit)\n (nil_lit)\n] @constant.builtin\n\n(kwd_lit) @constant\n\n(comment) @comment\n\n;; Treat quasiquotation as operators for the purpose of highlighting.\n\n[\n \"'\"\n \"~\"\n \",\"\n] @operator\n",
	})
}
