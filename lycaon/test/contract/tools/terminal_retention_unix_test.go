//go:build !windows

package contract

import (
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestTerminalPTYRetainsConfiguredBound(t *testing.T) {
	const retained = 64
	cfg := bgprocess.DefaultConfig()
	cfg.RingBufferBytes = retained
	bg := bgprocess.NewRegistry(cfg, bgprocess.Hooks{})
	handle, err := bg.Terminal.StartPTY(t.Context(), "session", "", "project", hostcmd.Request{
		Launch: lycexec.HostLaunch("terminal retention contract"), ProjectDir: t.TempDir(),
		Stages: []lycexec.Stage{{Name: "sh", Args: []string{"-c", "printf '%0256d' 1"}}},
	}, hostcmd.NewRunner(), lycexec.WinSize{}, confine.SpawnFacts{})
	contractcheck.FailErr(t, "start PTY", err)
	t.Cleanup(func() {
		_, closeErr := bg.Terminal.ClosePTY("session", handle)
		contractcheck.FailErr(t, "close PTY", closeErr)
	})
	settled, err := bg.Lifecycle.Await(t.Context(), "session", handle, 10*time.Second)
	contractcheck.FailErr(t, "await PTY", err)
	if !settled {
		t.Fatal("PTY did not settle")
	}
	// Request more than the configured retention to distinguish the storage
	// bound from the caller's read-page bound.
	out, err := bg.Terminal.ReadPTY("session", handle, bgprocess.PTYReadOpts{MaxBytes: 4096})
	contractcheck.FailErr(t, "read PTY", err)
	if out.Text != strings.Repeat("0", retained-1)+"1" || out.BytesReturned != retained {
		t.Fatalf("retained PTY output = %+v", out)
	}
	if !out.Truncated || out.EvictedBytes != 256-retained || out.NextCursor != 256 || out.PageContinuation {
		t.Fatalf("PTY retention provenance = %+v", out)
	}
	if out.Running || out.ExitCode == nil || *out.ExitCode != 0 {
		t.Fatalf("PTY exit = %+v", out)
	}
}
