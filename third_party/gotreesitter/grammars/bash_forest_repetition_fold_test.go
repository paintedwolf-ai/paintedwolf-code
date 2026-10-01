package grammars

import (
	"os"
	"path/filepath"
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

// TestBashForestRepetitionFoldDispatch guards the forest-side mirror of C's
// repetition-skip fold. Bash program bodies are repeated statement lists; if the
// forest engine forks {repetition SHIFT, REDUCE} at every statement boundary
// instead of folding the list spine, it reaches EOF without an accepting
// closed-list lineage and declines eof_no_root.
func TestBashForestRepetitionFoldDispatch(t *testing.T) {
	path := filepath.Join("..", "cgo_harness", "corpus_real", "bash", "medium__install.sh")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("bash corpus fixture unavailable: %v", err)
	}

	parser := ts.NewParser(BashLanguage())
	parser.SetTimeoutMicros(30 * 1000 * 1000)
	tree, ok := parser.ParseForestExperimental(src)
	if !ok || tree == nil {
		_, _, reason, _ := parser.ForestDeclineInfo()
		t.Fatalf("forest declined %s (reason=%q); want dispatch via repetition-skip fold", filepath.Base(path), reason)
	}
	t.Cleanup(tree.Release)

	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("forest tree HasError for %s; want clean dispatch", filepath.Base(path))
	}
	if got, want := root.ChildCount(), 65; got != want {
		t.Fatalf("forest root childCount=%d, want %d (C-oracle shape)", got, want)
	}
}
