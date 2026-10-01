//go:build grammar_subset && grammar_subset_authzed

package grammars

func init() {
	registerTokenSourceFactory("authzed", NewAuthzedTokenSourceOrEOF)
}
