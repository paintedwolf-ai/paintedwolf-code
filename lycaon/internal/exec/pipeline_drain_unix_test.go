//go:build unix

package exec

import (
	"context"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPipelineEscapedPipeHelper(t *testing.T) {
	switch os.Getenv("PW_PIPE_HELPER") {
	case "holder":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "leader":
		child := osexec.Command(os.Args[0], "-test.run=^TestPipelineEscapedPipeHelper$")
		child.Env = append(os.Environ(), "PW_PIPE_HELPER=holder")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		testutil.FailErr(t, "start detached pipe holder", child.Start())
		testutil.FailErr(t, "record pipe holder", os.WriteFile(os.Getenv("PW_PIPE_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0600))
		os.Exit(0)
	}
}

func TestAsyncPipelineSettlesAfterEscapedOutputHolder(t *testing.T) {
	binary, err := os.Executable()
	testutil.FailErr(t, "locate test binary", err)
	pidFile := filepath.Join(t.TempDir(), "holder.pid")
	t.Cleanup(func() {
		raw, readErr := os.ReadFile(pidFile)
		if readErr == nil {
			pid, parseErr := strconv.Atoi(string(raw))
			if parseErr == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	run, err := StartPipelineAsync(context.Background(), []Stage{{Name: binary, Args: []string{"-test.run=^TestPipelineEscapedPipeHelper$"}}}, ExecOpts{
		Launch: HostLaunch("pipe drain regression"), InlineEnv: map[string]string{"PW_PIPE_HELPER": "leader", "PW_PIPE_PID": pidFile},
	}, io.Discard, io.Discard)
	testutil.FailErr(t, "start escaped-pipe pipeline", err)
	t.Cleanup(run.Kill)
	select {
	case <-run.Done():
	case <-time.After(PipelineWaitDelay + 10*time.Second):
		t.Fatal("leader exited but async pipeline did not settle")
	}
	result, err := run.Wait()
	testutil.FailErr(t, "settle successful leader", err)
	if result.ExitCode != 0 || result.Stages[0].ExitStatus() != 0 || result.Stages[0].Failed {
		t.Fatalf("successful leader misreported: %+v", result)
	}
}
