//go:build !grammar_subset || grammar_subset_less

// Code generated from tree-sitter parser.c; DO NOT EDIT.
// Source: https://github.com/rhino1998/tree-sitter-less 2bd739e106a3485bca210cf7b6d25ba09fd10dff src/parser.c

package grammars

// lessExternalLexStates mirrors C tree-sitter ts_external_scanner_states.
var lessExternalLexStates = [][]bool{
	/* 0 */ {false},
	/* 1 */ {true},
}

func init() {
	RegisterExternalLexStates("less", lessExternalLexStates)
}
