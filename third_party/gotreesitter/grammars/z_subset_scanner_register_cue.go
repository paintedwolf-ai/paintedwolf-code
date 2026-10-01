//go:build grammar_subset && grammar_subset_cue

package grammars

func init() {
	RegisterExternalScanner("cue", CueExternalScanner{})
}
