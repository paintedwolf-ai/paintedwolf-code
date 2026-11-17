package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// sessionDeleteClients must call the shared write path.
var sessionDeleteClients = []string{
	"lycaon/internal/session/store/sql_sessions.go",
}

func TestSessionDeleteClientsUseWritePath(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, rel := range sessionDeleteClients {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		contractcheck.FailErr(t, "read "+rel, err)
		if !strings.Contains(string(src), "DeleteSessionTree(") {
			t.Fatalf("%s deletes sessions without calling db.DeleteSessionTree", rel)
		}
	}
}
