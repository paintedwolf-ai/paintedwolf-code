package exec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPTYIsattyDifferential(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix openpty")
	}
	if _, err := os.Stat("/bin/test"); err != nil {
		t.Skip("/bin/test not available")
	}

	_, pipeCode, pipeErr := Run(context.Background(), "/bin/test", []string{"-t", "1"}, ExecOpts{Launch: HostLaunch("exec test"),
		Timeout:        5 * time.Second,
		MaxOutputBytes: 1024,
	})
	if pipeErr != nil && pipeCode == 0 {
		testutil.FailErr(t, "pipe test -t 1", pipeErr)
	}
	if pipeCode == 0 {
		t.Fatal("pipe path: test -t 1 unexpectedly saw a tty")
	}

	_, ptyCode, ptyErr := RunPTY(context.Background(), "/bin/test", []string{"-t", "1"}, PTYOpts{Launch: HostLaunch("pty test"),
		Timeout:        5 * time.Second,
		MaxOutputBytes: 1024,
	})
	testutil.FailErr(t, "pty test -t 1", ptyErr)
	if ptyCode != 0 {
		t.Fatalf("pty path: test -t 1 exit = %d, want 0 (tty)", ptyCode)
	}
}

func TestRunPTYCaptureCap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix openpty")
	}
	maxOut := 256
	out, code, err := RunPTY(context.Background(), "python3", []string{"-c", "import sys; sys.stdout.write('x' * 10000); sys.stdout.flush()"}, PTYOpts{Launch: HostLaunch("pty test"),
		Timeout:        10 * time.Second,
		MaxOutputBytes: maxOut,
	})
	if !errors.Is(err, ErrOutputTruncated) {
		t.Fatalf("err = %v, want ErrOutputTruncated (exit=%d outLen=%d)", err, code, len(out))
	}
	if len(out) > maxOut {
		t.Fatalf("captured %d bytes, cap %d", len(out), maxOut)
	}
}

func TestRunPTYTimeoutKillsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix openpty")
	}
	prog, args, ok := spawnTreeHog(t)
	if !ok {
		t.Skip("no interpreter for process-tree test")
	}
	start := time.Now()
	_, _, err := RunPTY(context.Background(), prog, args, PTYOpts{Launch: HostLaunch("pty test"),
		Timeout:        200 * time.Millisecond,
		MaxOutputBytes: 4096,
	})
	if err == nil {
		t.Fatal("expected timeout")
	}
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want timed out", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout took too long; pty child process tree may still be running")
	}
}

func TestRunPTYSanitizesEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix openpty")
	}
	script := filepath.Join(t.TempDir(), "env.sh")
	body := "#!/bin/sh\nprintf '%s' \"${LD_PRELOAD:-}|${GIT_TRACE:-}|${NODE_OPTIONS:-}|ok\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		testutil.FailErr(t, "write env.sh", err)
	}
	out, code, err := RunPTY(context.Background(), script, nil, PTYOpts{Launch: HostLaunch("pty test"),
		Timeout: 5 * time.Second,
		Env: []string{
			"PATH=" + os.Getenv("PATH"),
			"LD_PRELOAD=/tmp/evil.so",
			"GIT_TRACE=1",
			"NODE_OPTIONS=--require evil",
			"HOME=/tmp",
		},
		MaxOutputBytes: 4096,
	})
	testutil.FailErr(t, "RunPTY env sanitize", err)
	if code != 0 {
		t.Fatalf("exit = %d out=%q", code, out)
	}
	got := strings.TrimSpace(string(out))
	// Script prints empty slots for stripped keys, then ok.
	if got != "|||ok" && got != "|||ok\r" {
		t.Fatalf("env not sanitized under pty: %q", got)
	}
}

func TestRunPTYPipePathUntouched(t *testing.T) {
	// Non-interactive execution uses pipes.
	_, code, err := Run(context.Background(), "echo", []string{"pipe-path"}, ExecOpts{Launch: HostLaunch("exec test"),
		Timeout:        5 * time.Second,
		MaxOutputBytes: 1024,
	})
	testutil.FailErr(t, "Run echo", err)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if _, err := os.Stat("/bin/test"); err != nil {
		return
	}
	_, code, _ = Run(context.Background(), "/bin/test", []string{"-t", "1"}, ExecOpts{Launch: HostLaunch("exec test"),
		Timeout: 5 * time.Second,
	})
	if code == 0 {
		t.Fatal("pipe Run unexpectedly has a tty after pty mode landed")
	}
}
