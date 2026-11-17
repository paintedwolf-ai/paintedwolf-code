package contract

import (
	"os/exec"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The macOS host staging and signing script ships with its own unittest suite;
// a missing interpreter fails here rather than passing unverified.
func TestMacOSHostProvisioningAuthority(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatalf("python3 is required to verify macOS host staging: %v", err)
	}
	cmd := exec.CommandContext(t.Context(), python, "-B", "-m", "unittest", "-v", "test_stage_macos_host")
	cmd.Dir = filepath.Join(contractcheck.RepoRoot(t), "scripts")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("macOS host provisioning authority: %v\n%s", err, output)
	}
}
