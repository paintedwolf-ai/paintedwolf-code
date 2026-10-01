//go:build grammar_subset && grammar_subset_tsx

package grammars

func init() {
	RegisterExternalScanner("tsx", TsxExternalScanner{})
}
