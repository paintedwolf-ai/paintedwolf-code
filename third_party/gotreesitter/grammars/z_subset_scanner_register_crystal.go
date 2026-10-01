//go:build grammar_subset && grammar_subset_crystal

package grammars

func init() {
	RegisterExternalScanner("crystal", CrystalExternalScanner{})
}
