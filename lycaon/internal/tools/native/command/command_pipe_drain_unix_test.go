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

// Cancelling a program whose external child left a detached holder of its
// output pipe still settles promptly and promotes nothing to background work.
func TestCancelledCommandDrainsEscapedOutputInline(t *testing.T) {
	binary, err := os.Executable()
	testutil.FailErr(t, "locate test binary", err)
	f := newProgramFixture(t)
	pidFile := filepath.Join(f.Dir, "holder.pid")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, runErr := f.call(t, ctx, "COMMAND_PIPE_FIXTURE_ROLE=leader COMMAND_PIPE_FIXTURE_PID="+strconv.Quote(pidFile)+" external -- "+strconv.Quote(binary)+" -test.run=TestCancelledCommandPipeHelper")
		result <- runErr
	}()
	var pid int
	testutil.WaitFor(t, func() bool {
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
	case <-t.Context().Done():
		t.Fatal("cancelled command did not settle")
	}
	if jobs := f.Background.ActiveJobs(f.Session); len(jobs) != 0 {
		t.Fatalf("cancelled leader promoted as background work: %+v", jobs)
	}
}
