package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func walkGoSources(t *testing.T, root string, visit func(rel, src string)) {
	t.Helper()
	repo := contractcheck.RepoRoot(t)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		rel, relErr := filepath.Rel(repo, path)
		if relErr != nil {
			return nil
		}
		visit(filepath.ToSlash(rel), string(raw))
		return nil
	})
	contractcheck.FailErr(t, "walk go files", err)
}
