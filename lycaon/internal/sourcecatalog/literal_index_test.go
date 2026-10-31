package sourcecatalog

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func literalCandidatePaths(entries []Entry) []string {
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}
	return paths
}

func writeLiteralFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	testutil.FailErr(t, "create fixture directory", os.MkdirAll(filepath.Dir(abs), 0o700))
	testutil.FailErr(t, "write fixture", os.WriteFile(abs, []byte(content), 0o600))
	noteLiteralFixtureWrite(abs)
	t.Cleanup(func() {
		literalFixtureVersions.Lock()
		delete(literalFixtureVersions.paths, abs)
		literalFixtureVersions.Unlock()
	})
}

func literalTestOpener(root string) func(Entry) (io.ReadCloser, error) {
	return func(entry Entry) (io.ReadCloser, error) {
		return os.Open(filepath.Join(root, filepath.FromSlash(entry.Path)))
	}
}
