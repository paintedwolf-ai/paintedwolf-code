//go:build grammar_subset && grammar_subset_starlark

package grammars

func init() {
	RegisterExternalScanner("starlark", StarlarkExternalScanner{})
}
