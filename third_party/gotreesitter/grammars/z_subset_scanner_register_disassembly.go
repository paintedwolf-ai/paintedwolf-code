//go:build grammar_subset && grammar_subset_disassembly

package grammars

func init() {
	RegisterExternalScanner("disassembly", DisassemblyExternalScanner{})
}
