package contract

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestHarnessSupervisorLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-B", "-m", "unittest", "-v", "test_supervise")
	cmd.Dir = filepath.Join(contractcheck.RepoRoot(t), "scripts", "harness")
	cmd.WaitDelay = 10 * time.Second
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("harness supervisor lifecycle: %v\n%s", err, output)
	}
}
