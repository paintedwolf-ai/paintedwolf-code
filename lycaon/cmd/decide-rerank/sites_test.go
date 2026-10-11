package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The search leg waits for the corpus root's inventory before it queries it.
func TestSearchDriverIndexesTheCorpusBeforeQuerying(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write corpus file", os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc rerankTarget() {}\n"), 0o644))
	c := &corpus{name: "corpus", root: root}

	driver, err := newSearchDriver(t.Context(), c)
	testutil.FailErr(t, "newSearchDriver", err)
	if len(driver.roots) != 1 || driver.roots[0].Path != root || driver.roots[0].ProjectID != c.name {
		t.Fatalf("roots = %+v, want the corpus root", driver.roots)
	}
	testutil.FailErr(t, "run search leg", driver.run(t.Context(), decide.Reranker{}, c, pair{Query: "rerankTarget"}))
	if err := driver.run(t.Context(), decide.Reranker{}, c, pair{Query: "  "}); err == nil {
		t.Fatal("a pair without a query must be skipped")
	}
}
