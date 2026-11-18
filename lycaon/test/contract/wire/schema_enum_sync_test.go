package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestSchemaDefinesCoreTables(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "lycaon", "internal", "db", "schema.sql"))
	contractcheck.FailErr(t, "read file", err)
	sql := string(data)
	for _, table := range []string{
		"projects", "sessions", "messages", "worker_jobs", "workers",
		"code_scans",
		"workflow_runs",
	} {
		if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS "+table+" (") && !strings.Contains(sql, "CREATE TABLE "+table+" (") {
			t.Fatalf("schema.sql missing table %q", table)
		}
	}
	if !strings.Contains(sql, "PRAGMA foreign_keys = ON") {
		t.Fatal("schema.sql should enable foreign keys")
	}
}
