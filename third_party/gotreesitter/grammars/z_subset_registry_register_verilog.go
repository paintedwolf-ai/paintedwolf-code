//go:build grammar_subset && grammar_subset_verilog

package grammars

func init() {
	Register(LangEntry{
		Name:           "verilog",
		Extensions:     []string{".v", ".sv", ".svh"},
		Language:       VerilogLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(module_keyword) @keyword\n(simple_identifier) @type\n",
	})
}
