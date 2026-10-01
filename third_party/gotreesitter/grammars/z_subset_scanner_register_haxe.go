//go:build grammar_subset && grammar_subset_haxe

package grammars

func init() {
	RegisterExternalScanner("haxe", HaxeExternalScanner{})
}
