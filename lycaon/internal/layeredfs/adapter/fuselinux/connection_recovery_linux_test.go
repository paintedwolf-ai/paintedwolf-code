//go:build linux

package fuselinux_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/fuselinux"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

const wedgedEngineHelperEnv = "LYCAON_FUSELINUX_WEDGED_ENGINE"

// TestProvenDeadServerWedgedOnItsOwnMountAbortsOnlyItsConnection reproduces F9 with a real engine
// [process-crash evidence, not physical power-loss proof]: a thread of the server waits on the
// server's own mount through a request the kernel will not interrupt, so SIGKILL leaves a zombie
// that holds the connection open. Only the recorded connection is aborted, and only once dead.
func TestProvenDeadServerWedgedOnItsOwnMountAbortsOnlyItsConnection(t *testing.T) {
	if os.Getenv(wedgedEngineHelperEnv) != "" {
		runWedgedEngine(t)
		return
	}
	tmp := t.TempDir()
	runtimeRoot := filepath.Join(tmp, "runtime")
	trunk := filepath.Join(tmp, "trunk")
	testutil.FailErr(t, "mkdir trunk", os.MkdirAll(trunk, 0o755))
	testutil.FailErr(t, "seed trunk", os.WriteFile(filepath.Join(trunk, "f"), []byte("x"), 0o644))
	exe, err := os.Executable()
	testutil.FailErr(t, "executable", err)
	engine := exec.Command(exe, "-test.run=^TestProvenDeadServerWedgedOnItsOwnMountAbortsOnlyItsConnection$")
	engine.Env = append(os.Environ(), wedgedEngineHelperEnv+"="+tmp)
	engine.Stderr = os.Stderr
	stdout, err := engine.StdoutPipe()
	testutil.FailErr(t, "stdout", err)
	stdin, err := engine.StdinPipe()
	testutil.FailErr(t, "stdin", err)
	defer func() { _ = stdin.Close() }()
	testutil.FailErr(t, "start engine", engine.Start())
	waited := make(chan error, 1)
	var mountPath string
	if _, err := fmt.Fscanf(stdout, "%s\n", &mountPath); err != nil {
		t.Fatalf("engine did not report its mount: %v", err)
	}
	go func() { waited <- engine.Wait() }()
	var connection string
	t.Cleanup(func() {
		_ = engine.Process.Kill()
		// A failed test still ends its own wedged connection, or the zombie outlives the run.
		if connection != "" {
			_ = os.WriteFile(filepath.Join("/sys/fs/fuse/connections", connection, "abort"), []byte("1"), 0o200)
		}
		_ = exec.Command("fusermount3", "-u", "-z", "--", mountPath).Run()
	})

	recorded, err := adapter.LoadRecordedConnections(filepath.Join(runtimeRoot, "view-wedged"))
	if err != nil || len(recorded) != 1 || recorded[0].DevMajorMinor == "" {
		t.Fatalf("recorded connections: %+v err=%v", recorded, err)
	}
	_, connection, _ = strings.Cut(recorded[0].DevMajorMinor, ":")
	waitFor(t, "two parked requests", func() bool { return waitingRequests(recorded[0].DevMajorMinor) >= 2 })

	adp := fuselinux.New(fuselinux.Config{})
	testutil.FailErr(t, "kill engine", engine.Process.Kill())
	procDir := filepath.Join("/proc", strconv.Itoa(engine.Process.Pid))
	waitFor(t, "the killed engine to be dying", func() bool { return fuselinux.ThreadGroupDyingForTest(procDir) })
	if mounted, err := adapter.MountBoundaryPresent(mountPath); err != nil || !mounted {
		t.Fatalf("a wedged mount must read as present without hanging: mounted=%t err=%v", mounted, err)
	}
	settled, err := adp.SettleRecordedConnection(t.Context(), recorded[0])
	if err != nil || settled.Disposition != adapter.ConnectionDetached || !settled.ConnectionAborted {
		t.Fatalf("settle wedged engine: %+v err=%v", settled, err)
	}
	select {
	case <-waited:
	case <-time.After(time.Minute):
		t.Fatal("aborting the connection did not release the wedged engine")
	}
	var st unix.Statfs_t
	if err := unix.Statfs(mountPath, &st); errors.Is(err, unix.ENOTCONN) {
		t.Fatal("dead mount still attached")
	}
	again, err := adp.SettleRecordedConnection(t.Context(), recorded[0])
	if err != nil || again.Disposition != adapter.ConnectionAbsent {
		t.Fatalf("retry must be idempotent: %+v err=%v", again, err)
	}
}

// runWedgedEngine parks a write in the single request worker, then an fsync, which FUSE waits on
// uninterruptibly, behind it.
func runWedgedEngine(t *testing.T) {
	tmp := os.Getenv(wedgedEngineHelperEnv)
	hold, parked := make(chan struct{}), make(chan struct{})
	var parkOnce sync.Once
	adp := fuselinux.New(fuselinux.Config{
		MaxConcurrentRequests: 1,
		OnMutationHook: func(uint32, string) {
			parkOnce.Do(func() { close(parked) })
			<-hold
		},
	})
	view, err := adp.Prepare(context.Background(), adapter.PrepareRequest{
		ViewID: "view-wedged", BackingDir: filepath.Join(tmp, "backing"), RuntimeDir: filepath.Join(tmp, "runtime"),
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: filepath.Join(tmp, "trunk"), IsPrimary: true}},
	})
	testutil.FailErr(t, "prepare", err)
	file, err := os.Open(filepath.Join(view.PrimaryWorkPath, "f"))
	testutil.FailErr(t, "open", err)
	fmt.Println(view.PrimaryWorkPath)
	go func() { _ = os.WriteFile(filepath.Join(view.PrimaryWorkPath, "other"), []byte("x"), 0o644) }()
	// The write must hold the only worker first, or the fsync is simply answered.
	<-parked
	go func() { _ = file.Sync() }()
	// A test that dies first must not strand a wedged process: unblocking lets this one exit.
	_, _ = io.Copy(io.Discard, os.Stdin)
	close(hold)
	os.Exit(0)
}

func waitingRequests(dev string) int {
	_, minor, _ := strings.Cut(dev, ":")
	raw, err := os.ReadFile(filepath.Join("/sys/fs/fuse/connections", minor, "waiting"))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return n
}

func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	for range 6000 {
		if done() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestThreadGroupDyingRequiresEveryThreadUnderSIGKILL(t *testing.T) {
	stat := func(state string) string { return "1 (x) " + state + " 0\n" }
	status := func(pending string) string { return "Name:\tx\nSigPnd:\t" + pending + "\n" }
	cases := []struct {
		name   string
		leader string
		tasks  map[string][2]string
		want   bool
	}{
		{"running, nothing pending", "R", map[string][2]string{"1": {stat("R"), status("0")}}, false},
		{"zombie leader, stuck thread under SIGKILL", "Z", map[string][2]string{"1": {stat("Z"), status("0")}, "2": {stat("D"), status("0000000000000100")}}, true},
		{"stuck leader under SIGKILL", "D", map[string][2]string{"1": {stat("D"), status("0000000000000100")}, "2": {stat("D"), status("0000000000000100")}}, true},
		{"leader exited, another thread still running", "Z", map[string][2]string{"1": {stat("Z"), status("0")}, "2": {stat("S"), status("0")}}, false},
		{"unreadable pending mask", "Z", map[string][2]string{"1": {stat("Z"), status("0")}, "2": {stat("D"), ""}}, false},
		{"no threads readable", "Z", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			testutil.FailErr(t, "stat", os.WriteFile(filepath.Join(dir, "stat"), []byte(stat(tc.leader)), 0o600))
			for tid, files := range tc.tasks {
				taskDir := filepath.Join(dir, "task", tid)
				testutil.FailErr(t, "task dir", os.MkdirAll(taskDir, 0o700))
				testutil.FailErr(t, "task stat", os.WriteFile(filepath.Join(taskDir, "stat"), []byte(files[0]), 0o600))
				testutil.FailErr(t, "task status", os.WriteFile(filepath.Join(taskDir, "status"), []byte(files[1]), 0o600))
			}
			if got := fuselinux.ThreadGroupDyingForTest(dir); got != tc.want {
				t.Fatalf("dying = %t want %t", got, tc.want)
			}
		})
	}
}

// TestMountHelperThatAttachedAndThenFailedLeavesNoMount: a helper can fail or be killed after the kernel
// attached the mount, and no server ever takes the connection, so what it left would be a dead mount.
func TestMountHelperThatAttachedAndThenFailedLeavesNoMount(t *testing.T) {
	tmp := t.TempDir()
	trunk, helper := filepath.Join(tmp, "trunk"), filepath.Join(tmp, "attach-then-fail")
	testutil.FailErr(t, "create trunk", os.MkdirAll(trunk, 0o755))
	python, err := exec.LookPath("python3")
	testutil.FailErr(t, "find python3", err)
	fusermount, err := exec.LookPath("fusermount3")
	testutil.FailErr(t, "find fusermount3", err)
	attached := filepath.Join(tmp, "attached")
	script := "#!" + python + `
import os, socket, subprocess, sys
keep, send = socket.socketpair(socket.AF_UNIX, socket.SOCK_SEQPACKET)
os.set_inheritable(send.fileno(), True)
subprocess.run(["` + fusermount + `", *sys.argv[1:]], env=dict(os.environ, _FUSE_COMMFD=str(send.fileno())), close_fds=False)
if any(line.split()[4] == sys.argv[-1] for line in open("/proc/self/mountinfo")):
    open("` + attached + `", "w").close()
sys.exit(1)
`
	testutil.FailErr(t, "write helper", os.WriteFile(helper, []byte(script), 0o755))
	runtimeDir := filepath.Join(tmp, "runtime")
	mountPath := filepath.Join(runtimeDir, "view-attached", "mounts", "primary")
	t.Cleanup(func() { _ = exec.Command(fusermount, "-u", "-z", "--", mountPath).Run() })

	_, err = fuselinux.New(fuselinux.Config{FusermountBin: helper}).Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: "view-attached", BackingDir: filepath.Join(tmp, "backing"), RuntimeDir: runtimeDir,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunk, IsPrimary: true}},
	})
	if err == nil {
		t.Fatal("expected Prepare to report the failed helper")
	}
	_, err = os.Stat(attached)
	testutil.FailErr(t, "helper attached the mount", err)
	// Rollback keeps the records while anything is still mounted below the view.
	recorded, err := adapter.LoadRecordedConnections(filepath.Join(runtimeDir, "view-attached"))
	if err != nil || len(recorded) != 0 {
		t.Fatalf("records after failed prepare: %+v err=%v", recorded, err)
	}
}
