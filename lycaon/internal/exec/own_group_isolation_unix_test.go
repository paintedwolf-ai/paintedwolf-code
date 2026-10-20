//go:build unix

package exec

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/osprocess"
)

func TestOwnedCancellationPreservesAnUnrelatedProcess(t *testing.T) {
	unrelated := exec.Command("sleep", "30")
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() }()
	before, ok := osprocess.StartTime(unrelated.Process.Pid)
	if !ok {
		t.Fatal("unrelated fixture identity unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	owned, cleanup, err := PrepareCommand(ctx, "sleep", []string{"30"}, ExecOpts{Launch: HostLaunch("synthetic cancellation fixture")})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := RunInOwnGroup(owned); err == nil {
		t.Fatal("canceled owned process reported success")
	}
	if ctx.Err() == nil {
		t.Fatal("owned process exited before cancellation")
	}
	after, current := osprocess.StartTime(unrelated.Process.Pid)
	if !current || before != after || syscall.Kill(unrelated.Process.Pid, 0) != nil {
		t.Fatal("owned cancellation affected unrelated fixture process")
	}
}
