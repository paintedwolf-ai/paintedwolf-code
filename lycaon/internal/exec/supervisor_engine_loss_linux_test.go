//go:build linux

package exec

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

const engineLossModeEnv = "PW_TEST_ENGINE_LOSS_MODE"
const engineLossDirEnv = "PW_TEST_ENGINE_LOSS_DIR"

func TestEngineLossFixtureProcess(t *testing.T) {
	mode := os.Getenv(engineLossModeEnv)
	if mode == "" {
		return
	}
	dir := os.Getenv(engineLossDirEnv)
	python, err := exec.LookPath("python3")
	testutil.FailErr(t, "locate engine loss fixture runtime", err)
	box := &confine.Confinement{HostExecution: true, Roots: []string{dir}, Network: confine.NetworkDirectIP}
	if mode == "confined" {
		if !confine.Available() {
			caps, _ := confine.LinuxCapabilities()
			t.Fatalf("engine loss fixture requires applied confinement: %+v", caps)
		}
		box = &confine.Confinement{Roots: []string{dir}, Network: confine.NetworkDeny, EgressOff: confine.EgressOffOverride}
	}
	script := fmt.Sprintf(`import os, signal, time
path = %s
if os.fork() == 0:
    os.setsid()
    if os.fork() == 0:
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        os.closerange(0, 256)
        with open(path, 'w') as file:
            file.write(str(os.getpid()))
        time.sleep(60)
    os._exit(0)
while not os.path.exists(path):
    time.sleep(0.01)
time.sleep(60)
`, strconv.Quote(filepath.Join(dir, "daemon.pid")))
	cmd, cleanup, err := PrepareCommand(context.Background(), python, []string{"-c", script}, ExecOpts{
		Launch: AgentLaunch(LaunchAgentCommand, "engine loss fixture", box).WithPathAuthority(confine.NativeLaunchPaths(dir)), Dir: dir,
	})
	testutil.FailErr(t, "prepare owned engine loss command", err)
	defer cleanup()
	testutil.FailErr(t, "run owned engine loss command", RunInOwnGroup(context.Background(), cmd))
}

func TestSupervisorEngineSIGKILLRemovesOnlyItsOwnCommands(t *testing.T) {
	other, otherCleanup, otherPath := supervisedDaemonCommand(t)
	defer otherCleanup()
	testutil.FailErr(t, "start unrelated command", StartPreparedWith(other, other.Start, TrackProcessGroup))
	otherPID := waitForPIDFile(t, otherPath)
	_, otherStart, ok := procStat(otherPID)
	if !ok {
		t.Fatal("unrelated command disappeared before the fixture")
	}
	defer func() { otherCleanup(); _ = other.Wait() }()
	self, err := os.Executable()
	testutil.FailErr(t, "engine fixture executable", err)
	for _, mode := range []string{"host_execution", "confined"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			engine := exec.CommandContext(t.Context(), self, "-test.run=^TestEngineLossFixtureProcess$")
			engine.Env = append(os.Environ(), engineLossModeEnv+"="+mode, engineLossDirEnv+"="+dir)
			var output bytes.Buffer
			engine.Stdout, engine.Stderr = &output, &output
			testutil.FailErr(t, "start fixture engine", engine.Start())
			done := make(chan struct{})
			go func() { _ = engine.Wait(); close(done) }()
			defer func() { _ = engine.Process.Kill(); <-done }()
			// The daemon's PID is namespace-local; ownership comes from this engine's host tree.
			awaitEngineDaemon(t, filepath.Join(dir, "daemon.pid"), engine, done, &output)
			owned := commandDescendants(engine.Process.Pid)
			if len(owned) < 2 {
				t.Fatalf("engine owned only %d processes before SIGKILL", len(owned))
			}
			defer killFixtureDescendants(owned)
			testutil.FailErr(t, "SIGKILL fixture engine", engine.Process.Kill())
			<-done
			status := engine.ProcessState.Sys().(syscall.WaitStatus)
			if !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatalf("engine exit status = %v, want SIGKILL", status)
			}
			testutil.FailErr(t, "engine-owned processes survived SIGKILL", waitForFixtureDescendantsGone(t.Context(), owned))
			if _, start, ok := procStat(otherPID); !ok || start != otherStart {
				t.Fatal("engine loss affected an unrelated command")
			}
		})
	}
}

func awaitEngineDaemon(t *testing.T, path string, engine *exec.Cmd, done <-chan struct{}, output *bytes.Buffer) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-done:
			t.Fatalf("fixture engine exited before daemon readiness: %v; %s", engine.ProcessState, output.String())
		case <-deadline.C:
			_ = engine.Process.Kill()
			<-done
			t.Fatalf("fixture engine never published daemon readiness: %s", output.String())
		case <-poll.C:
			raw, err := os.ReadFile(path)
			if err == nil {
				if pid, err := strconv.Atoi(string(raw)); err == nil && pid > 0 {
					return
				}
			}
		}
	}
}

func waitForFixtureDescendantsGone(ctx context.Context, owned []descendant) error {
	for ctx.Err() == nil {
		remaining := 0
		for _, child := range owned {
			if _, start, ok := procStat(child.pid); ok && start == child.start {
				remaining++
			}
		}
		if remaining == 0 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("engine-owned process identities remain: %w", ctx.Err())
}

func killFixtureDescendants(owned []descendant) {
	for _, child := range owned {
		fd, err := unix.PidfdOpen(child.pid, 0)
		if err != nil {
			continue
		}
		if _, start, ok := procStat(child.pid); ok && start == child.start {
			_ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
		}
		_ = unix.Close(fd)
	}
}
