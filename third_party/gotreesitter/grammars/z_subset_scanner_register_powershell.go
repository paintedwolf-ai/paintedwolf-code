//go:build grammar_subset && grammar_subset_powershell

package grammars

func init() {
	RegisterExternalScanner("powershell", PowershellExternalScanner{})
}
