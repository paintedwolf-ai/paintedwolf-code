//go:build grammar_subset && grammar_subset_kconfig

package grammars

func init() {
	RegisterExternalScanner("kconfig", KconfigExternalScanner{})
}
