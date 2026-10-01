//go:build grammar_subset && grammar_subset_json

package grammars

func init() {
	registerTokenSourceFactory("json", NewJSONTokenSourceOrEOF)
}
