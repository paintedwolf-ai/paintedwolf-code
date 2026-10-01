package contract

import (
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestProjectSpaceDenGreenfieldEnforcement(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	scan, err := scanProjectSpaceDen(root)
	contractcheck.FailErr(t, "scan den project-space enforcement", err)
	if len(scan.Violations) > 0 {
		t.Fatalf("project-space den violations:\n%s", strings.Join(scan.Violations, "\n"))
	}
}

func TestNoHardcodedAgentsMDWrites(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	scan, err := scanHardcodedAgentsMDWrites(filepath.Join(root, "lycaon"))
	contractcheck.FailErr(t, "scan AGENTS.md writes", err)
	if len(scan.Violations) > 0 {
		t.Fatalf("hardcoded AGENTS.md write violations:\n%s", strings.Join(scan.Violations, "\n"))
	}
}
