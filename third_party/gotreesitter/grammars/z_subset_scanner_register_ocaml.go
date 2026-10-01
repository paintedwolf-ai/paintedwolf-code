//go:build grammar_subset && grammar_subset_ocaml

package grammars

func init() {
	RegisterExternalScanner("ocaml", OcamlExternalScanner{})
}
