package bgprocess

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/confine"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStartPTYDriveSendReadClose(t *testing.T) {
	if runtime.GOOS == "windows" {
		reg := newTestRegistry(t, DefaultConfig(), Hooks{})
		runner := hostcmd.NewRunner()
		_, err := reg.StartPTY(context.Background(), "s1", "", "p1", hostcmd.Request{Launch: lycexec.HostLaunch("bgprocess pty test"),
			ProjectDir: t.TempDir(),
			Stages:     []lycexec.Stage{{Name: "echo", Args: []string{"hi"}}},
		}, runner, lycexec.WinSize{}, confine.SpawnFacts{})
		if !errors.Is(err, lycexec.ErrPTYUnsupported) {
			t.Fatalf("err = %v, want ErrPTYUnsupported", err)
		}
		return
	}

	reg := newTestRegistry(t, DefaultConfig(), Hooks{})
	dir := t.TempDir()
	runner := hostcmd.NewRunner()
	// Script: print prompt, read a line, echo it back. Keep shell metacharacters
	// in the file body so hostcmd ValidateStages sees clean argv.
	scriptPath := filepath.Join(dir, "drive.sh")
	body := "#!/bin/sh\nprintf 'PROMPT> '\nIFS= read -r line\nprintf 'GOT:%s\\n' \"$line\"\n"
	if err := os.WriteFile(scriptPath, []byte(body), 0o755); err != nil {
		testutil.FailErr(t, "write drive.sh", err)
	}
	handle, err := reg.StartPTY(context.Background(), "s1", "", "p1", hostcmd.Request{Launch: lycexec.HostLaunch("bgprocess pty test"),
		ProjectDir: dir,
		Stages:     []lycexec.Stage{{Name: scriptPath}},
	}, runner, lycexec.WinSize{}, confine.SpawnFacts{})
	testutil.FailErr(t, "StartPTY", err)
	defer func() { _, _ = reg.ClosePTY("s1", handle) }()

	// Polling accumulates output from ReadPTY's persistent cursor.
	readUntil := func(step, marker string) {
		t.Helper()
		var buf strings.Builder
		if !testutil.WaitForNoFatal(20*time.Second, func() bool {
			res, err := reg.ReadPTY("s1", handle, PTYReadOpts{Idle: 50 * time.Millisecond, Timeout: 250 * time.Millisecond})
			if err != nil {
				return false
			}
			buf.WriteString(res.Text)
			return strings.Contains(buf.String(), marker)
		}) {
			t.Fatalf("%s: %q never contained %q", step, buf.String(), marker)
		}
	}
	readUntil("prompt", "PROMPT>")
	testutil.FailErr(t, "WritePTY", reg.WritePTY("s1", handle, []byte("yes\r")))
	readUntil("answer", "GOT:yes")

	_, err = reg.ClosePTY("s1", handle)
	testutil.FailErr(t, "ClosePTY", err)
	if err := reg.RequireRunning("s1", handle); !errors.Is(err, ErrProcessNotFound) {
		t.Fatalf("after close RequireRunning = %v, want not found", err)
	}
}

func TestDisposeSessionReapsPTY(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix openpty")
	}
	reg := newTestRegistry(t, DefaultConfig(), Hooks{})
	runner := hostcmd.NewRunner()
	handle, err := reg.StartPTY(context.Background(), "s1", "", "p1", hostcmd.Request{Launch: lycexec.HostLaunch("bgprocess pty test"),
		ProjectDir: t.TempDir(),
		Stages:     []lycexec.Stage{{Name: "sh", Args: []string{"-c", "while true; do sleep 1; done"}}},
	}, runner, lycexec.WinSize{}, confine.SpawnFacts{})
	testutil.FailErr(t, "StartPTY", err)

	testutil.FailErr(t, "dispose session PTYs", reg.DisposeSession(context.Background(), "s1"))
	if err := reg.RequireRunning("s1", handle); !errors.Is(err, ErrProcessNotFound) {
		t.Fatalf("after DisposeSession RequireRunning = %v, want not found", err)
	}
}

func TestReadPTYIdleNoHang(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix openpty")
	}
	reg := newTestRegistry(t, DefaultConfig(), Hooks{})
	runner := hostcmd.NewRunner()
	handle, err := reg.StartPTY(context.Background(), "s1", "", "p1", hostcmd.Request{Launch: lycexec.HostLaunch("bgprocess pty test"),
		ProjectDir: t.TempDir(),
		Stages:     []lycexec.Stage{{Name: "sh", Args: []string{"-c", "printf 'hi'; sleep 30"}}},
	}, runner, lycexec.WinSize{}, confine.SpawnFacts{})
	testutil.FailErr(t, "StartPTY", err)
	defer func() { _, _ = reg.ClosePTY("s1", handle) }()

	start := time.Now()
	res, err := reg.ReadPTY("s1", handle, PTYReadOpts{Idle: 100 * time.Millisecond, Timeout: 2 * time.Second})
	testutil.FailErr(t, "ReadPTY", err)
	if time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("read hung too long: %v", time.Since(start))
	}
	if !strings.Contains(res.Text, "hi") {
		t.Fatalf("text = %q", res.Text)
	}
	if !res.Running {
		t.Fatal("expected still running after idle read")
	}
}

func TestRunPTYCaptureReturnsSettledScreenWithoutHandle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix openpty")
	}
	reg := newTestRegistry(t, DefaultConfig(), Hooks{})
	configurePTYCaptureProjection(reg)
	result, err := reg.RunPTYCapture(t.Context(), "s1", "", "p1", hostcmd.Request{
		Launch: lycexec.HostLaunch("sealed pty capture test"), ProjectDir: t.TempDir(),
		Stages: []lycexec.Stage{{Name: "sh", Args: []string{"-c", "printf 'READY\\n'"}}},
	}, hostcmd.NewRunner(), lycexec.WinSize{Cols: 80, Rows: 24}, confine.SpawnFacts{}, 5*time.Second)
	testutil.FailErr(t, "RunPTYCapture", err)
	if result.ExitCode != 0 || result.TimedOut || !strings.Contains(strings.Join(result.Screen.Lines, "\n"), "READY") {
		t.Fatalf("capture = %+v", result)
	}
	if got := reg.List(context.Background(), "s1"); len(got) != 0 {
		t.Fatalf("sealed capture leaked handles: %+v", got)
	}
}

func TestSilentPTYCaptureUsesAwaitedCapOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix openpty")
	}
	cfg := DefaultConfig()
	cfg.MaxAwaited = 1
	cfg.MaxBackground = 1
	reg := newTestRegistry(t, cfg, Hooks{})
	runner := hostcmd.NewRunner()
	req := hostcmd.Request{
		Launch:     lycexec.HostLaunch("sealed pty admission test"),
		ProjectDir: t.TempDir(),
		Stages:     []lycexec.Stage{{Name: "sh", Args: []string{"-c", "sleep 30"}}},
	}

	sealed, err := reg.startPTY(t.Context(), "s1", "", "p1", req, runner, lycexec.WinSize{}, confine.SpawnFacts{}, true)
	testutil.FailErr(t, "start sealed PTY", err)
	defer func() { _, _ = reg.ClosePTY("s1", sealed) }()

	if _, err := reg.startPTY(t.Context(), "s1", "", "p1", req, runner, lycexec.WinSize{}, confine.SpawnFacts{}, true); !errors.Is(err, ErrAwaitedCapReached) {
		t.Fatalf("second sealed PTY error = %v want %v", err, ErrAwaitedCapReached)
	}
	visible, err := reg.StartPTY(t.Context(), "s1", "", "p1", req, runner, lycexec.WinSize{}, confine.SpawnFacts{})
	testutil.FailErr(t, "start visible PTY", err)
	defer func() { _, _ = reg.ClosePTY("s1", visible) }()

	reg.mu.Lock()
	awaited := reg.countRunningAwaitedLocked("s1")
	background := reg.countRunningBackgroundLocked("s1")
	reg.mu.Unlock()
	if got := awaited; got != 1 {
		t.Fatalf("awaited count = %d want 1", got)
	}
	if got := background; got != 1 {
		t.Fatalf("background count = %d want 1", got)
	}
}
