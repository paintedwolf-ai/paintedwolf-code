//go:build grammar_subset && grammar_subset_comment

package grammars

func init() {
	RegisterExternalScanner("comment", CommentExternalScanner{})
}
