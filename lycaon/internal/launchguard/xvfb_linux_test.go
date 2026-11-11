//go:build linux

package launchguard

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

const (
	fixtureXvfbReadinessDeadline = 30 * time.Second
	fixtureXvfbStopGrace         = 2 * time.Second
)

func startFixtureXvfb(t *testing.T) string {
	t.Helper()
	display, err := startFixtureDisplayProcess(
		t,
		fixtureXvfbReadinessDeadline,
		"Xvfb",
		"-noreset",
		"-displayfd", "3",
		"-screen", "0", "800x600x24",
		"-nolisten", "tcp",
	)
	testutil.FailErr(t, "receive own X display number", err)
	return display
}

func startFixtureDisplayProcess(t testing.TB, deadline time.Duration, name string, args ...string) (string, error) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		return "", fmt.Errorf("create display readiness pipe: %w", err)
	}
	defer func() {
		_ = reader.Close()
		_ = writer.Close()
	}()
	logPath := filepath.Join(t.TempDir(), "xvfb.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return "", fmt.Errorf("retain display diagnostics: %w", err)
	}
	defer func() { _ = logFile.Close() }()
	command := exec.Command(name, args...)
	command.ExtraFiles = []*os.File{writer}
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		return "", fmt.Errorf("start isolated X display: %w", err)
	}
	_ = writer.Close()
	_ = logFile.Close()

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- command.Wait()
	}()
	var (
		stopOnce sync.Once
		stopNote string
	)
	stopProcess := func(onFailure bool) string {
		stopOnce.Do(func() {
			stopNote = terminateDisplayProcess(command.Process, waitDone, onFailure)
		})
		return stopNote
	}

	if err := reader.SetReadDeadline(time.Now().Add(deadline)); err != nil {
		status := stopProcess(true)
		return "", formatDisplayReadinessError("bound display startup", err, status, logPath)
	}
	text, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		status := stopProcess(true)
		return "", formatDisplayReadinessError("read displayfd", err, status, logPath)
	}
	number, err := strconv.ParseUint(strings.TrimSpace(text), 10, 16)
	if err != nil {
		status := stopProcess(true)
		return "", formatDisplayReadinessError(fmt.Sprintf("decode display number %q", strings.TrimSpace(text)), err, status, logPath)
	}
	t.Cleanup(func() {
		_ = stopProcess(false)
	})
	return fmt.Sprintf(":%d", number), nil
}

func terminateDisplayProcess(process *os.Process, waitDone <-chan error, onFailure bool) string {
	if onFailure {
		select {
		case err := <-waitDone:
			if err != nil {
				return fmt.Sprintf("exited before readiness (%v)", err)
			}
			return "exited before readiness (status 0)"
		case <-time.After(100 * time.Millisecond):
		}
	} else {
		select {
		case err := <-waitDone:
			if err != nil {
				return fmt.Sprintf("already exited (%v)", err)
			}
			return "already exited (status 0)"
		default:
		}
	}
	_ = process.Signal(unix.SIGTERM)
	select {
	case err := <-waitDone:
		if err != nil {
			return fmt.Sprintf("running; stopped with SIGTERM (%v)", err)
		}
		return "running; stopped with SIGTERM (status 0)"
	case <-time.After(fixtureXvfbStopGrace):
		_ = process.Kill()
		err := <-waitDone
		return fmt.Sprintf("running; killed after SIGTERM timeout (%v)", err)
	}
}

func formatDisplayReadinessError(step string, cause error, processStatus, logPath string) error {
	diagnostic, readErr := os.ReadFile(logPath)
	text := strings.TrimSpace(string(diagnostic))
	if readErr != nil {
		text = fmt.Sprintf("<read xvfb.log: %v>", readErr)
	} else if text == "" {
		text = "<empty>"
	}
	return fmt.Errorf("%s: %w (process: %s; xvfb.log: %s)", step, cause, processStatus, text)
}

func TestFixtureXvfbReadinessAndCleanShutdown(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "display.pid")
	var display string
	t.Run("subtest", func(t *testing.T) {
		var err error
		display, err = startFixtureDisplayProcess(
			t,
			5*time.Second,
			"sh", "-c",
			`echo $$ > "$1"; printf '42\n' >&3; trap 'exit 0' TERM; while :; do sleep 1; done`,
			"sh", pidFile,
		)
		testutil.FailErr(t, "start synthetic display process", err)
		if display != ":42" {
			t.Fatalf("unexpected display %q, want :42", display)
		}
	})
	rawPID, err := os.ReadFile(pidFile)
	testutil.FailErr(t, "read synthetic display pid", err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	testutil.FailErr(t, "parse synthetic display pid", err)
	if err := unix.Kill(pid, 0); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("display process %d still alive after cleanup: %v", pid, err)
	}

	var socketPath string
	t.Run("real-xvfb", func(t *testing.T) {
		liveDisplay := startFixtureXvfb(t)
		number := strings.TrimPrefix(liveDisplay, ":")
		if number == "" || number == liveDisplay {
			t.Fatalf("invalid live Xvfb display %q", liveDisplay)
		}
		socketPath = fmt.Sprintf("/tmp/.X11-unix/X%s", number)
		if _, err := os.Stat(socketPath); err != nil {
			t.Fatalf("expected active Xvfb socket %s: %v", socketPath, err)
		}
	})
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("expected Xvfb SIGTERM cleanup to unlink %s, got err=%v", socketPath, err)
	}
}

func TestFixtureXvfbEarlyExitIncludesDiagnostics(t *testing.T) {
	_, err := startFixtureDisplayProcess(
		t,
		5*time.Second,
		"sh", "-c",
		`echo "Fatal server error: cannot establish any listening sockets" >&2; exit 1`,
	)
	if err == nil {
		t.Fatal("expected error when display process exits before writing displayfd")
	}
	message := err.Error()
	for _, expected := range []string{
		"EOF",
		"exited before readiness (exit status 1)",
		"xvfb.log: Fatal server error: cannot establish any listening sockets",
	} {
		if !strings.Contains(message, expected) {
			t.Fatalf("expected error %q to contain %q", message, expected)
		}
	}
}

func TestFixtureXvfbReadinessTimeoutTerminatesChildAndReportsDiagnostics(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "stalled.pid")
	_, err := startFixtureDisplayProcess(
		t,
		150*time.Millisecond,
		"sh", "-c",
		`echo $$ > "$1"; echo "xkbcomp: compiling keymap" >&2; exec sleep 30`,
		"sh", pidFile,
	)
	if err == nil {
		t.Fatal("expected timeout error when display process holds displayfd open without newline")
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("expected deadline exceeded error, got: %v", err)
	}
	message := err.Error()
	for _, expected := range []string{
		"running; stopped with SIGTERM",
		"xvfb.log: xkbcomp: compiling keymap",
	} {
		if !strings.Contains(message, expected) {
			t.Fatalf("expected error %q to contain %q", message, expected)
		}
	}
	rawPID, err := os.ReadFile(pidFile)
	testutil.FailErr(t, "read stalled display pid", err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	testutil.FailErr(t, "parse stalled display pid", err)
	if err := unix.Kill(pid, 0); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("stalled display process %d still alive after timeout cleanup: %v", pid, err)
	}
}
