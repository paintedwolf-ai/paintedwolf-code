package contract

import (
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestNoHardcodedAgentsMDWrites(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	scan, err := scanHardcodedAgentsMDWrites(filepath.Join(root, "lycaon"))
	contractcheck.FailErr(t, "scan AGENTS.md writes", err)
	if len(scan.Violations) > 0 {
		t.Fatalf("hardcoded AGENTS.md write violations:\n%s", strings.Join(scan.Violations, "\n"))
	}
}
