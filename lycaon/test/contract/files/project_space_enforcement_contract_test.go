package contract

import (
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
