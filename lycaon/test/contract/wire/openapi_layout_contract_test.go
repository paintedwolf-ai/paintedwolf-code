package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestOpenAPIRootEntrypoint(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	entry := filepath.Join(root, "docs", "openapi", "root.yaml")
	if _, err := os.Stat(entry); err != nil {
		t.Fatalf("missing %s: %v", entry, err)
	}
	data, err := os.ReadFile(entry)
	contractcheck.FailErr(t, "read file", err)
	text := string(data)
	if !strings.Contains(text, "openapi: 3.1.0") {
		t.Fatal("root.yaml missing openapi version")
	}
	if !strings.Contains(text, "paths:") || !strings.Contains(text, "$ref: paths/") {
		t.Fatal("root.yaml must reference paths/ fragments")
	}
}
