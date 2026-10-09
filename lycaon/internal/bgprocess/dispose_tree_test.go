//go:build unix

package bgprocess_test

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/osprocess"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A dev server started from a shell with job control lives in its own process
// group and keeps the job's output pipe open; disposing the session must end
// it and return within the stop budget.
func TestDisposeSessionEndsJobsOutsideTheLeadersGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "job.pid")
	reg := newTestRegistry(t, bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{})
	_, err := startBackground(context.Background(), reg, "sess-1", "proj-1", hostcmd.Request{
		Launch: exec.HostLaunch("bgprocess test"), ProjectDir: dir,
		Stages: []exec.Stage{{Name: "sh", Args: []string{"-c",
			fmt.Sprintf("set -m; sleep 30 & echo $! > %s; wait", strconv.Quote(pidFile))}}},
	}, hostcmd.NewRunner())
	testutil.FailErr(t, "start background", err)
	job := waitForJobPID(t, pidFile)

	started := time.Now()
	testutil.FailErr(t, "dispose session processes", reg.Lifecycle.DisposeSession(context.Background(), "sess-1"))
	if elapsed := time.Since(started); elapsed > exec.TerminateGrace+5*time.Second {
		t.Fatalf("dispose took %v", elapsed)
	}
	testutil.WaitFor(t, 5*time.Second, func() bool { return !osprocess.Alive(job) })
}

func waitForJobPID(t *testing.T, path string) int {
	t.Helper()
	var pid int
	testutil.WaitFor(t, 20*time.Second, func() bool {
		raw, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(raw)))
		return err == nil && pid > 0
	})
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

func TestDetachedOutputHolder(t *testing.T) {
	switch os.Getenv("PW_BG_PIPE_HELPER") {
	case "holder":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "leader":
		child := osexec.Command(os.Args[0], "-test.run=^TestDetachedOutputHolder$")
		child.Env = append(os.Environ(), "PW_BG_PIPE_HELPER=holder")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		testutil.FailErr(t, "start detached output holder", child.Start())
		testutil.FailErr(t, "record detached output holder", os.WriteFile(os.Getenv("PW_BG_PIPE_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0600))
		os.Exit(0)
	}
}

func TestDisposeSessionWaitsForBoundedDetachedOutputDrain(t *testing.T) {
	binary, err := os.Executable()
	testutil.FailErr(t, "locate test binary", err)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "holder.pid")
	reg := newTestRegistry(t, bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{})
	_, err = startBackground(context.Background(), reg, "sess-1", "proj-1", hostcmd.Request{
		Launch: exec.HostLaunch("detached output regression"), ProjectDir: dir,
		IOParams: hostcmd.IOParams{InlineEnv: map[string]string{"PW_BG_PIPE_HELPER": "leader", "PW_BG_PIPE_PID": pidFile}},
		Stages:   []exec.Stage{{Name: binary, Args: []string{"-test.run=^TestDetachedOutputHolder$"}}},
	}, hostcmd.NewRunner())
	testutil.FailErr(t, "start background with detached output holder", err)
	_ = waitForJobPID(t, pidFile)
	ctx, cancel := context.WithTimeout(context.Background(), exec.TerminateGrace+exec.PipelineWaitDelay+5*time.Second)
	defer cancel()
	testutil.FailErr(t, "dispose detached output drain", reg.DisposeSession(ctx, "sess-1"))
}
