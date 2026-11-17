package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestComposeEffectiveSummaryRequiresIsolationField(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	openapi, err := os.ReadFile(filepath.Join(root, "docs", "openapi.yaml"))
	contractcheck.FailErr(t, "read file", err)
	if !strings.Contains(string(openapi), "requires_isolation") {
		t.Fatal("openapi missing requires_isolation on ComposeEffectiveSummary")
	}
	ts, err := os.ReadFile(filepath.Join(root, "lycaon-den", "src", "api", "types.ts"))
	contractcheck.FailErr(t, "read file", err)
	if !strings.Contains(string(ts), "requires_isolation") {
		t.Fatal("types.ts missing requires_isolation")
	}
}
