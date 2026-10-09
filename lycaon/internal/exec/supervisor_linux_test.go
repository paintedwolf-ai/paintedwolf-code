//go:build linux

package exec

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func detachedDaemonCommand(t *testing.T, ctx context.Context, stay bool) (*exec.Cmd, func(), string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	testutil.FailErr(t, "locate daemon fixture runtime", err)
	path := filepath.Join(t.TempDir(), "daemon.pid")
	script := fmt.Sprintf(`import os, signal, time
path = %s
if os.fork() == 0:
    os.setsid()
    if os.fork() == 0:
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        with open(path, 'w') as file:
            file.write(str(os.getpid()))
        time.sleep(60)
    os._exit(0)
while not os.path.exists(path):
    time.sleep(0.01)
%s
`, strconv.Quote(path), map[bool]string{true: "time.sleep(60)", false: ""}[stay])
	cmd, cleanup, err := PrepareCommand(ctx, python, []string{"-c", script}, ExecOpts{Launch: HostLaunch("detached daemon test")})
	testutil.FailErr(t, "prepare detached command", err)
	return cmd, cleanup, path
}

func supervisedDaemonCommand(t *testing.T) (*exec.Cmd, func(), string) {
	t.Helper()
	cmd, cleanup, path := detachedDaemonCommand(t, context.Background(), true)
	cmd, cleanup, err := superviseCommand(cmd, cleanup, ProcessPriorityNormal)
	testutil.FailErr(t, "supervise daemon fixture", err)
	return cmd, cleanup, path
}

func TestSupervisorCleansDoubleForkedNewSessionOnCommandCompletion(t *testing.T) {
	cmd, cleanup, path := detachedDaemonCommand(t, context.Background(), false)
	defer cleanup()
	testutil.FailErr(t, "run daemon launcher", RunInOwnGroup(cmd))
	pid := waitForPIDFile(t, path)
	testutil.FailErr(t, "detached daemon survived completion", waitForProcessGone(pid, 5*time.Second))
}

func TestSupervisorCancellationSparesAnotherCommandsDetachedDaemon(t *testing.T) {
	other, otherCleanup, otherPath := supervisedDaemonCommand(t)
	defer otherCleanup()
	testutil.FailErr(t, "start independent supervisor", other.Start())
	otherPID := waitForPIDFile(t, otherPath)
	defer func() { otherCleanup(); _ = other.Wait() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, cleanup, path := detachedDaemonCommand(t, ctx, true)
	defer cleanup()
	done := make(chan error, 1)
	go func() { done <- RunInOwnGroup(cmd) }()
	pid := waitForPIDFile(t, path)
	cancel()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("cancelled supervisor did not settle")
	}
	testutil.FailErr(t, "cancelled detached daemon survived", waitForProcessGone(pid, 5*time.Second))
	if err := waitForProcessGone(otherPID, 100*time.Millisecond); err == nil {
		t.Fatal("cancellation killed a different command's daemon")
	}
}

func TestSupervisorEngineLifetimeEOFRemovesDetachedDaemon(t *testing.T) {
	cmd, cleanup, path := supervisedDaemonCommand(t)
	defer cleanup()
	testutil.FailErr(t, "start supervisor", cmd.Start())
	pid := waitForPIDFile(t, path)
	cleanup()
	_ = cmd.Wait()
	testutil.FailErr(t, "daemon survived engine lifetime EOF", waitForProcessGone(pid, 5*time.Second))
}

func TestSupervisorCompanionRequestsOwnedTreeCleanup(t *testing.T) {
	cmd, cleanup, path := supervisedDaemonCommand(t)
	defer cleanup()
	testutil.FailErr(t, "start supervised command", cmd.Start())
	pid := waitForPIDFile(t, path)
	reapLines(fmt.Sprintf("+s%d\n", cmd.Process.Pid))
	_ = cmd.Wait()
	testutil.FailErr(t, "daemon survived companion cleanup", waitForProcessGone(pid, 5*time.Second))
}

func TestGuardedPipelinePriorityReachesPrimary(t *testing.T) {
	python, err := exec.LookPath("python3")
	testutil.FailErr(t, "locate priority fixture", err)
	inherited, err := processNice(os.Getpid())
	testutil.FailErr(t, "read inherited priority", err)
	result, err := RunPipeline(t.Context(), []Stage{{Name: python, Args: []string{"-c", "import os; print(os.getpid(), os.getpriority(os.PRIO_PROCESS, 0))"}}}, ExecOpts{
		Launch: HostLaunch("priority inheritance regression"), ProcessPriority: ProcessPriorityBelowNormal,
	})
	testutil.FailErr(t, "run priority fixture", err)
	var pid, nice int
	_, err = fmt.Sscanf(string(result.Output), "%d %d", &pid, &nice)
	testutil.FailErr(t, "read primary priority", err)
	if result.ExitCode != 0 || pid <= 0 || nice != max(inherited, DefaultBelowNormalNice) {
		t.Fatalf("primary priority: pid=%d nice=%d inherited=%d result=%+v", pid, nice, inherited, result)
	}
}

func TestSupervisedPTYInteractiveShellOwnsForeground(t *testing.T) {
	bash, err := exec.LookPath("bash")
	testutil.FailErr(t, "locate interactive shell", err)
	terminal, err := StartPTY(t.Context(), bash, []string{"--noprofile", "--norc", "-i"}, PTYOpts{Launch: HostLaunch("interactive terminal regression"), Timeout: 10 * time.Second})
	testutil.FailErr(t, "start interactive terminal", err)
	defer terminal.Close()
	done := make(chan string, 1)
	go func() { bytes, _ := io.ReadAll(terminal); done <- string(bytes) }()
	_, err = terminal.Write([]byte("printf '__foreground_ok__\\n'; exit\n"))
	testutil.FailErr(t, "send interactive terminal input", err)
	testutil.FailErr(t, "wait interactive shell", terminal.Wait())
	select {
	case output := <-done:
		if !strings.Contains(output, "__foreground_ok__\r\n") || strings.Contains(output, "no job control") {
			t.Fatalf("interactive shell did not control terminal: %q", output)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal output did not settle")
	}
}

func TestSupervisedCommandSeesOnlyItsIntendedDescriptors(t *testing.T) {
	python, err := exec.LookPath("python3")
	testutil.FailErr(t, "locate descriptor fixture runtime", err)
	// fstat probes descriptors without opening one, unlike listing /proc/self/fd.
	const probe = `import os
open_fds = []
for fd in range(256):
    try:
        os.fstat(fd)
        open_fds.append(str(fd))
    except OSError:
        pass
print(" ".join(open_fds))`
	for name, extra := range map[string]int{"standard streams only": 0, "one extra file": 1} {
		t.Run(name, func(t *testing.T) {
			cmd, cleanup, err := PrepareCommand(t.Context(), python, []string{"-c", probe}, ExecOpts{Launch: HostLaunch("descriptor inheritance regression")})
			testutil.FailErr(t, "prepare descriptor probe", err)
			defer cleanup()
			want := "0 1 2"
			if extra > 0 {
				read, write, err := os.Pipe()
				testutil.FailErr(t, "open intended descriptor", err)
				defer func() { _ = read.Close(); _ = write.Close() }()
				cmd.ExtraFiles = []*os.File{write}
				want += " 3"
			}
			var out strings.Builder
			cmd.Stdout = &out
			testutil.FailErr(t, "run supervised descriptor probe", RunInOwnGroup(cmd))
			if got := strings.TrimSpace(out.String()); got != want {
				t.Fatalf("supervised command descriptors = %q, want %q", got, want)
			}
		})
	}
}
