//go:build grammar_subset && grammar_subset_fortran

package grammars

func init() {
	RegisterExternalScanner("fortran", FortranExternalScanner{})
}
