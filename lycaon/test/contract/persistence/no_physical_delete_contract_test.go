package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestNoDeleteFromMessagesInLifecycleSQL(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	packages := []string{
		filepath.Join(root, "lycaon", "internal", "session"),
		filepath.Join(root, "lycaon", "internal", "coordinator", "promptloop"),
	}
	for _, pkgDir := range packages {
		err := filepath.WalkDir(pkgDir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(body), "func (s *SQL) DeleteMessage") {
				t.Errorf("%s: session store must not define DeleteMessage (append-only lifecycle)", path)
			}
			if strings.Contains(string(body), "DELETE FROM messages") {
				t.Errorf("%s: lifecycle path must not DELETE FROM messages", path)
			}
			return nil
		})
		testutil.FailErr(t, "walk "+pkgDir, err)
	}
}
