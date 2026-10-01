package survey

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func testSummarizeGatherer(t *testing.T, dir string, caps summarize.Caps) *summarizeGatherer {
	t.Helper()
	caps.Gather.IndexWaitMs = 30_000
	g := &summarizeGatherer{
		boundary: nativefixture.Boundary(t),
		reads:    projectpaths.NewReadSession(nativefixture.Boundary(t), nativefixture.Context(dir)),
		caps:     caps,
		tctx:     nativefixture.Context(dir),
	}
	t.Cleanup(g.closeTrees)
	t.Cleanup(g.reads.Close)
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
