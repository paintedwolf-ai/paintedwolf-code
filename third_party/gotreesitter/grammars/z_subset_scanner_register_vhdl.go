//go:build grammar_subset && grammar_subset_vhdl

package grammars

func init() {
	RegisterExternalScanner("vhdl", VhdlExternalScanner{})
}
