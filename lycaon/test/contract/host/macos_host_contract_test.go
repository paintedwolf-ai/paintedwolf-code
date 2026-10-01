package contract

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestMacOSHostProvisioningAuthority(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-B", "-m", "unittest", "-v", "test_stage_macos_host")
	cmd.Dir = filepath.Join(contractcheck.RepoRoot(t), "scripts")
	cmd.WaitDelay = 10 * time.Second
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("macOS host provisioning authority: %v\n%s", err, output)
	}
}
