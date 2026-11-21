package contract

import (
	"os/exec"
	"path/filepath"
	"testing"

	lyexec "github.com/lycaon/lycaon/internal/exec"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestZxcvbnPasswordsVendorCheckMatchesLedger(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	cmd := exec.Command(filepath.Join(root, "scripts", "vendor-zxcvbn-passwords.sh"), "--check")
	cmd.Dir = root
	cmd.Env = lyexec.LocalGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("vendor-zxcvbn-passwords.sh --check failed: %v\n%s", err, out)
	}
}
