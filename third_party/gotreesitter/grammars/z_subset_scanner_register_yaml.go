//go:build grammar_subset && grammar_subset_yaml

package grammars

func init() {
	RegisterExternalScanner("yaml", YamlExternalScanner{})
	RegisterExternalLexStates("yaml", yamlExternalLexStates)
}
