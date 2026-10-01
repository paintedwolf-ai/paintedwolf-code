//go:build grammar_subset && grammar_subset_luau

package grammars

func init() {
	RegisterExternalScanner("luau", LuauExternalScanner{})
}
