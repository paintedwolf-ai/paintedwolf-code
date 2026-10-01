//go:build grammar_subset && grammar_subset_jsonnet

package grammars

func init() {
	RegisterExternalScanner("jsonnet", JsonnetExternalScanner{})
}
