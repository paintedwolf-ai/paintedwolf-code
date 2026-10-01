//go:build grammar_subset && grammar_subset_disassembly

package grammars

func init() {
	Register(LangEntry{
		Name:           "disassembly",
		Extensions:     []string{".dis", ".dump"},
		Language:       DisassemblyLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(byte) @constant\n\n[\n  (address)\n  (hexadecimal)\n  (integer)\n] @number\n\n(identifier) @variable\n\n(bad_instruction) @text.warning\n(code_location (identifier) @function.call)\n(comment) @comment\n(instruction) @function\n(memory_dump) @string\n\n[\"<\" \">\"] @punctuation.special\n[\"+\" \":\"] @punctuation.delimiter\n",
	})
}
