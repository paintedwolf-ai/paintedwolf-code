//go:build grammar_subset && (grammar_subset_c || grammar_subset_cpp)

package grammars

func init() {
	registerTokenSourceFactory("c", NewCTokenSourceOrEOF)
	registerTokenSourceFactory("cpp", NewCTokenSourceOrEOF)
}
