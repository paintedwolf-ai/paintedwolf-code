package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	lyexec "github.com/lycaon/lycaon/internal/exec"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestZxcvbnPasswordsVendorTreeHoldsLicenseAndList(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "security", "host", "secret-mint", "vendor", "zxcvbn-passwords")
	for _, name := range []string{"LICENSE", "passwords.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s missing from vendored tree", name)
		}
	}
}

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
