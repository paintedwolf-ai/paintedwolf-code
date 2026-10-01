//go:build grammar_subset && grammar_subset_toml

package grammars

func init() {
	RegisterExternalScanner("toml", TomlExternalScanner{})
}
