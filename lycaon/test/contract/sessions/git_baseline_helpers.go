package contract

import (
	"os/exec"
	"strings"
	"testing"

	lyexec "github.com/lycaon/lycaon/internal/exec"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// lastReleasedTagRef returns the most recent v* tag reachable from HEAD, or ""
// when none exists. On trunk the merge base is HEAD, so the last release tag is
// the baseline for "changed since the last installed build".
func lastReleasedTagRef(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("git", "describe", "--tags", "--abbrev=0", "--match", "v*")
	cmd.Dir = contractcheck.RepoRoot(t)
	cmd.Env = lyexec.LocalGitEnv()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitShowFile(t *testing.T, rev, relPath string) ([]byte, bool) {
	t.Helper()
	cmd := exec.Command("git", "show", rev+":"+relPath)
	cmd.Dir = contractcheck.RepoRoot(t)
	cmd.Env = lyexec.LocalGitEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return out, true
}
