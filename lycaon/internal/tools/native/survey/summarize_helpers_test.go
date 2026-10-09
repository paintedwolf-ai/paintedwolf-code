package survey

import (
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"os"
	"path/filepath"
	"testing"
)

func testSummarizeGatherer(t *testing.T, dir string, caps summarize.Caps) *summarizeGatherer {
	t.Helper()
	caps.Gather.IndexWaitMs = 30_000
	boundary := nativefixture.Boundary(t)
	tctx := nativefixture.Context(dir)
	reads := projectpaths.NewReadSession(boundary, tctx)
	g := newSummarizeGatherer(boundary, reads, caps, tctx, nil, decide.Reranker{})
	t.Cleanup(g.trees.closeTrees)
	t.Cleanup(reads.Close)
	return g
}

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}
