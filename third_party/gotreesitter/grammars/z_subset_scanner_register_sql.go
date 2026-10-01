//go:build grammar_subset && grammar_subset_sql

package grammars

func init() {
	RegisterExternalScanner("sql", SqlExternalScanner{})
}
