//go:build unix

package command_test

import (
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCancelledCommandPipeHelper(t *testing.T) {
	switch os.Getenv("COMMAND_PIPE_FIXTURE_ROLE") {
	case "holder":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "leader":
		child := osexec.Command(os.Args[0], "-test.run=^TestCancelledCommandPipeHelper$")
		child.Env = append(os.Environ(), "COMMAND_PIPE_FIXTURE_ROLE=holder")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		testutil.FailErr(t, "start detached command output holder", child.Start())
		testutil.FailErr(t, "record detached command output holder", os.WriteFile(os.Getenv("COMMAND_PIPE_FIXTURE_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0600))
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

func TestCancelledCommandDrainsEscapedOutputInline(t *testing.T) {
	binary, err := os.Executable()
	testutil.FailErr(t, "locate test binary", err)
	root := t.TempDir()
	pidFile := filepath.Join(root, "holder.pid")
	tool, registry := newCommandTool(t)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		testutil.FailErr(t, "close command registry", registry.Lifecycle.Close(ctx))
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, runErr := tool.Run(ctx, map[string]any{
			"command": strconv.Quote(binary) + " -test.run=TestCancelledCommandPipeHelper",
			"wait_ms": float64(10000),
			"env":     map[string]any{"COMMAND_PIPE_FIXTURE_ROLE": "leader", "COMMAND_PIPE_FIXTURE_PID": pidFile},
		}, commandToolContext(root, "pipe-cancel", ""))
		result <- runErr
	}()
	var pid int
	testutil.WaitFor(t, 10*time.Second, func() bool {
		select {
		case runErr := <-result:
			t.Fatalf("command returned before fixture spawned: %v", runErr)
		default:
		}
		raw, readErr := os.ReadFile(pidFile)
		if readErr != nil {
			return false
		}
		pid, readErr = strconv.Atoi(string(raw))
		return readErr == nil && pid > 0
	})
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel command: %v", err)
		}
	case <-time.After(exec.TerminateGrace + exec.PipelineWaitDelay + 10*time.Second):
		t.Fatal("cancelled command did not settle")
	}
	if jobs := registry.ActiveJobs("pipe-cancel"); len(jobs) != 0 {
		t.Fatalf("cancelled leader promoted as background work: %+v", jobs)
	}
}
