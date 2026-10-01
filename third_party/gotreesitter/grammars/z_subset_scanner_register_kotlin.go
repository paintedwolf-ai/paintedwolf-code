//go:build grammar_subset && grammar_subset_kotlin

package grammars

func init() {
	RegisterExternalScanner("kotlin", KotlinExternalScanner{})
}
