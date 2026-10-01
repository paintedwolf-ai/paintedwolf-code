package exec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRunEcho(t *testing.T) {
	out, code, err := Run(context.Background(), "echo", []string{"hello"}, ExecOpts{Launch: HostLaunch("exec test"),
		Timeout:        5 * time.Second,
		MaxOutputBytes: 1024,
	})
	testutil.FailErr(t, "Run failed", err)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if string(out) != "hello\n" && string(out) != "hello\r\n" {
		t.Fatalf("output = %q", out)
	}
}

func TestRunRejectsShellMetacharactersInName(t *testing.T) {
	_, _, err := Run(context.Background(), "echo; rm -rf /", nil, ExecOpts{Launch: HostLaunch("exec test")})
	if err == nil {
		t.Fatal("expected metachar rejection")
	}
}

func TestRunPassesArgsVerbatim(t *testing.T) {
	out, code, err := Run(context.Background(), "echo", []string{"a; b | c > d"}, ExecOpts{Launch: HostLaunch("exec test"),
		Timeout:        5 * time.Second,
		MaxOutputBytes: 1024,
	})
	testutil.FailErr(t, "Run failed", err)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(string(out), "a; b | c > d") {
		t.Fatalf("output = %q", out)
	}
}

func TestRunTimeout(t *testing.T) {
	if _, err := os.Stat("/bin/sleep"); err != nil {
		t.Skip("sleep not available")
	}
	_, _, err := Run(context.Background(), "sleep", []string{"2"}, ExecOpts{Launch: HostLaunch("exec test"),
		Timeout: 50 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected timeout")
	}
}

func TestRunInDir(t *testing.T) {
	dir := t.TempDir()
	name := "pwd_marker.txt"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("ok"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	out, code, err := Run(context.Background(), "ls", []string{name}, ExecOpts{Launch: HostLaunch("exec test"), Dir: dir})
	if err != nil || code != 0 {
		t.Fatalf("ls in dir: %v code=%d", err, code)
	}
	if len(out) == 0 {
		t.Fatal("expected ls output")
	}
}

func TestRunOutputTruncation(t *testing.T) {
	out, code, err := Run(context.Background(), "python3", []string{"-c", "print('x' * 2000)"}, ExecOpts{Launch: HostLaunch("exec test"),
		Timeout:        5 * time.Second,
		MaxOutputBytes: 1024,
	})
	if err == nil {
		t.Fatal("expected truncation error")
	}
	if !errors.Is(err, ErrOutputTruncated) {
		t.Fatalf("expected ErrOutputTruncated, got %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if len(out) != 1024 {
		t.Fatalf("captured bytes = %d, want 1024", len(out))
	}
}

func TestRunKeepOutputTail(t *testing.T) {
	out, code, err := Run(context.Background(), "python3", []string{"-c", "import sys; sys.stdout.write('A'*1500); sys.stdout.write('TAILMARK')"}, ExecOpts{Launch: HostLaunch("exec test"),
		Timeout:        5 * time.Second,
		MaxOutputBytes: 64,
		KeepOutputTail: true,
	})
	if err == nil {
		t.Fatal("expected truncation error")
	}
	if !errors.Is(err, ErrOutputTruncated) {
		t.Fatalf("expected ErrOutputTruncated, got %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if len(out) != 64 {
		t.Fatalf("captured bytes = %d, want 64", len(out))
	}
	if !strings.HasSuffix(string(out), "TAILMARK") {
		t.Fatalf("tail missing marker: %q", out)
	}
	if strings.Contains(string(out), "AAAA") && strings.HasPrefix(string(out), "A") && !strings.Contains(string(out), "TAILMARK") {
		t.Fatal("kept head instead of tail")
	}
}

func TestRunTimeoutKillsChildProcess(t *testing.T) {
	prog, args, ok := spawnTreeHog(t)
	if !ok {
		t.Skip("no interpreter for process-tree test")
	}
	start := time.Now()
	_, _, err := Run(context.Background(), prog, args, ExecOpts{Launch: HostLaunch("exec test"),
		Timeout:        200 * time.Millisecond,
		MaxOutputBytes: 4096,
	})
	if err == nil {
		t.Fatal("expected timeout")
	}
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout took too long; child process tree may still be running")
	}
}

func spawnTreeHog(t *testing.T) (string, []string, bool) {
	t.Helper()
	switch runtime.GOOS {
	case "windows":
		return "powershell", []string{
			"-NoProfile", "-Command",
			"Start-Process -WindowStyle Hidden -FilePath powershell -ArgumentList '-NoProfile','-Command','Start-Sleep -Seconds 120'",
			"Start-Sleep -Seconds 120",
		}, true
	default:
		script := filepath.Join(t.TempDir(), "hog.py")
		body := "import subprocess, time\nsubprocess.Popen(['sleep', '120'])\ntime.sleep(120)\n"
		if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
			testutil.FailErr(t, "write hog.py", err)
		}
		for _, name := range []string{"python3", "python"} {
			py, err := exec.LookPath(name)
			if err == nil {
				return py, []string{script}, true
			}
		}
		return "", nil, false
	}
}
