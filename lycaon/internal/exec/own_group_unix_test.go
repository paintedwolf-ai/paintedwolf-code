//go:build unix

package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

const terminalHelperEnv = "PW_TEST_TERMINAL_HELPER"

func TestRunInOwnGroupEndsWhatTheCommandLeftBehind(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "helper.pid")
	cmd, cleanup, err := PrepareCommand(context.Background(), "sh",
		[]string{"-c", fmt.Sprintf("sleep 30 & echo $! > %s", strconv.Quote(pidFile))},
		ExecOpts{Launch: HostLaunch("exec test")})
	testutil.FailErr(t, "prepare", err)
	defer cleanup()

	testutil.FailErr(t, "run", RunInOwnGroup(cmd))
	helper := waitForPIDFile(t, pidFile)
	if err := waitForProcessGone(helper, 5*time.Second); err != nil {
		_ = syscall.Kill(helper, syscall.SIGKILL)
		testutil.FailErr(t, "helper outlived the command that started it", err)
	}
}

// Interactive shells ignore SIGTERM.
func TestRunInOwnGroupCancellationKillsAShellIgnoringTerm(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "helper.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	cmd, cleanup, err := PrepareCommand(ctx, "sh",
		[]string{"-c", fmt.Sprintf("trap '' TERM; sleep 30 & echo $! > %s; wait", strconv.Quote(pidFile))},
		ExecOpts{Launch: HostLaunch("exec test")})
	testutil.FailErr(t, "prepare", err)
	defer cleanup()

	started := time.Now()
	err = RunInOwnGroup(cmd)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run error = %v, want the killed shell's exit", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
	helper := waitForPIDFile(t, pidFile)
	if err := waitForProcessGone(helper, 5*time.Second); err != nil {
		_ = syscall.Kill(helper, syscall.SIGKILL)
		testutil.FailErr(t, "helper outlived cancellation", err)
	}
}

// An engine started from a shell holds that terminal; a child that reads it
// fails fast instead of stopping as a background job.
func TestRunInOwnGroupDetachesFromTheEnginesTerminal(t *testing.T) {
	result := filepath.Join(t.TempDir(), "result")
	helper := exec.Command(os.Args[0], "-test.run=^TestRunInOwnGroupTerminalHelperProcess$")
	helper.Env = append(os.Environ(), terminalHelperEnv+"="+result)
	terminal, err := startWithPTY(helper, WinSize{Cols: 80, Rows: 24})
	testutil.FailErr(t, "start helper under a terminal", err)
	defer terminal.Close()
	go func() { _, _ = io.Copy(io.Discard, terminal) }()

	done := make(chan error, 1)
	go func() { done <- helper.Wait() }()
	select {
	case err := <-done:
		testutil.FailErr(t, "helper", err)
	case <-time.After(20 * time.Second):
		_ = helper.Process.Kill()
		t.Fatal("helper never finished")
	}
	raw, err := os.ReadFile(result)
	testutil.FailErr(t, "read helper result", err)
	if got := strings.TrimSpace(string(raw)); got != "answered" {
		t.Fatalf("command touching the terminal: %s, want it to answer", got)
	}
}

// TestRunInOwnGroupTerminalHelperProcess is the engine holding a terminal, not
// a test. It runs only when the parent case starts it under a pty.
func TestRunInOwnGroupTerminalHelperProcess(t *testing.T) {
	result := os.Getenv(terminalHelperEnv)
	if result == "" {
		t.Skip("helper process for TestRunInOwnGroupDetachesFromTheEnginesTerminal")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd, cleanup, err := PrepareCommand(ctx, "sh",
		[]string{"-c", `if (: < /dev/tty) 2>/dev/null; then read -r line < /dev/tty; fi; echo answered`},
		ExecOpts{Launch: HostLaunch("exec test")})
	testutil.FailErr(t, "prepare", err)
	defer cleanup()
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	err = RunInOwnGroup(cmd)
	outcome := strings.TrimSpace(stdout.String())
	switch {
	case ctx.Err() != nil:
		outcome = "stopped until canceled"
	case err != nil:
		outcome = "failed: " + err.Error()
	}
	testutil.FailErr(t, "write result", os.WriteFile(result, []byte(outcome), 0o600))
}
