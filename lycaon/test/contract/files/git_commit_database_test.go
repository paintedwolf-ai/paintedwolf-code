package contract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/tools/native"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestGitCommitStagesProjectDatabaseFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	gittest.Init(t, root)
	const content = "SQLite format 3\x00fixture bytes\x00"
	paths := []string{"testdata/upgrade-corpus/store.db", "testdata/upgrade-corpus/store.db-wal", "testdata/upgrade-corpus/store.db-shm"}
	contractcheck.FailErr(t, "create fixture directory", os.MkdirAll(filepath.Join(root, "testdata/upgrade-corpus"), 0o755))
	for _, path := range paths {
		contractcheck.FailErr(t, "write binary database fixture", os.WriteFile(filepath.Join(root, path), []byte(content), 0o644))
	}
	hash, _, err := native.GitMutations{Git: git.NewManager(), Boundary: contractcheck.ProdToolBoundary(t)}.Commit(t.Context(), implementToolContext(root), native.GitCommitRequest{Message: "Record database fixtures", Paths: paths})
	contractcheck.FailErr(t, "commit database fixtures", err)
	if hash == "" {
		t.Fatal("database fixture commit returned no hash")
	}
	for _, path := range paths {
		if got := gittest.Run(t, root, "show", "HEAD:"+path); got != content {
			t.Errorf("committed %s = %q, want %q", path, got, content)
		}
	}
}
