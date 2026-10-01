package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestDBSQLCConfigPointsAtSchemaSSOT(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "lycaon", "internal", "db", "sqlc.yaml"))
	contractcheck.FailErr(t, "read sqlc.yaml", err)
	text := string(data)
	for _, required := range []string{
		"engine: \"sqlite\"",
		"schema: \"schema.sql\"",
		"queries: \"queries\"",
		"package: \"db\"",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("sqlc.yaml missing %q", required)
		}
	}
}
