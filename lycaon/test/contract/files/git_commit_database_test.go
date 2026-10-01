package contract

import (
	"encoding/json"
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
	paths := []any{"testdata/upgrade-corpus/store.db", "testdata/upgrade-corpus/store.db-wal", "testdata/upgrade-corpus/store.db-shm"}
	contractcheck.FailErr(t, "create fixture directory", os.MkdirAll(filepath.Join(root, "testdata/upgrade-corpus"), 0o755))
	for _, path := range paths {
		contractcheck.FailErr(t, "write binary database fixture", os.WriteFile(filepath.Join(root, path.(string)), []byte(content), 0o644))
	}
	tool := &native.GitCommitTool{Git: git.NewManager(), Boundary: contractcheck.ProdToolBoundary(t)}
	out, err := tool.Run(t.Context(), map[string]any{
		"message": "Record database fixtures", "paths": paths,
	}, implementToolContext(root))
	contractcheck.FailErr(t, "commit database fixtures", err)
	var receipt struct {
		Available bool `json:"available"`
	}
	contractcheck.FailErr(t, "decode commit receipt", json.Unmarshal([]byte(out), &receipt))
	if !receipt.Available {
		t.Fatalf("database fixture commit unavailable: %s", out)
	}
	for _, path := range paths {
		if got := gittest.Run(t, root, "show", "HEAD:"+path.(string)); got != content {
			t.Errorf("committed %s = %q, want %q", path, got, content)
		}
	}
}
