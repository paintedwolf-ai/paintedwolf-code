package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/osprocess"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A copy of this test binary stands in for the desktop: TestMain acts out the
// behaviour named by PW_FAKE_DESKTOP instead of running tests.
const fakeDesktopEnv = "PW_FAKE_DESKTOP"

func TestMain(m *testing.M) {
	switch os.Getenv(fakeDesktopEnv) {
	case "":
		os.Exit(m.Run())
	case "listen":
		listener, err := net.Listen("unix", os.Getenv("PW_FAKE_SOCKET"))
		if err != nil {
			os.Exit(9)
		}
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				_ = conn.Close()
			}
		}()
	case "fail":
		os.Exit(3)
	case "stubborn":
		signal.Ignore(syscall.SIGTERM)
		if path := os.Getenv("PW_FAKE_CHILD_READY"); path != "" {
			_ = os.WriteFile(path, []byte("ready"), 0o600)
		}
	case "stubborn-family":
		self, _ := os.Executable()
		child := exec.Command(self)
		child.Env = append(os.Environ(), fakeDesktopEnv+"=stubborn")
		if child.Start() != nil {
			os.Exit(9)
		}
		_ = os.WriteFile(os.Getenv("PW_FAKE_CHILD"), []byte(strconv.Itoa(child.Process.Pid)), 0o600)
	case "family":
		child := exec.Command("sleep", "60")
		if child.Start() != nil {
			os.Exit(9)
		}
		_ = os.WriteFile(os.Getenv("PW_FAKE_CHILD"), []byte(strconv.Itoa(child.Process.Pid)), 0o600)
	}
	time.Sleep(time.Minute)
	os.Exit(0)
}

type fakeDesktop struct {
	t      *testing.T
	base   string
	socket string
	launch desktopLaunch
}

func newFakeDesktop(t *testing.T, behaviour string) *fakeDesktop {
	t.Helper()
	base := t.TempDir()
	// t.TempDir honors the host umask; the installed launcher's directories are 0755.
	if err := os.Chmod(base, 0o755); err != nil {
		testutil.FailErr(t, "os.Chmod failed", err)
	}
	bin := filepath.Join(base, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		testutil.FailErr(t, "os.Mkdir failed", err)
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		testutil.FailErr(t, "os.Chmod failed", err)
	}
	self, err := os.Executable()
	testutil.FailErr(t, "os.Executable failed", err)
	launcher := filepath.Join(bin, "painted-wolf-code")
	copyFile(t, self, launcher)
	socketDir, err := os.MkdirTemp("", "pwcli") //nolint:usetesting // Unix socket paths have a short limit.
	testutil.FailErr(t, "os.MkdirTemp failed", err)
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "s")
	environment := append(os.Environ(), fakeDesktopEnv+"="+behaviour, "PW_FAKE_SOCKET="+socket,
		"PW_FAKE_CHILD="+filepath.Join(base, "child.pid"),
		"PW_FAKE_CHILD_READY="+filepath.Join(base, "child.ready"), "WAYLAND_DISPLAY=wayland-test")
	return &fakeDesktop{t: t, base: base, socket: socket, launch: desktopLaunch{
		path: launcher, anchor: base, owner: uint32(os.Getuid()), environment: environment, grace: 200 * time.Millisecond}}
}

func copyFile(t *testing.T, source, target string) {
	t.Helper()
	in, err := os.Open(source)
	testutil.FailErr(t, "os.Open failed", err)
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	testutil.FailErr(t, "os.OpenFile failed", err)
	if _, err := io.Copy(out, in); err != nil {
		testutil.FailErr(t, "io.Copy failed", err)
	}
	if err := out.Close(); err != nil {
		testutil.FailErr(t, "out.Close failed", err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		testutil.FailErr(t, "os.Chmod failed", err)
	}
}

func (f *fakeDesktop) start(ctx context.Context, timeout time.Duration) (net.Conn, *desktopStart, error) {
	var started *desktopStart
	conn, err := dialOrStartAt(ctx, f.socket, func(context.Context) (appStart, error) {
		start, err := startDesktop(f.launch)
		if start != nil {
			started = start.(*desktopStart)
			f.t.Cleanup(started.Stop)
		}
		return start, err
	}, timeout, 20*time.Millisecond)
	return conn, started, err
}

func groupGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process group %d is still alive", pid)
}

func TestColdStartReachesTheStartedDesktopInItsOwnSession(t *testing.T) {
	fake := newFakeDesktop(t, "listen")
	conn, started, err := fake.start(context.Background(), 20*time.Second)
	testutil.FailErr(t, "fake.start failed", err)
	_ = conn.Close()
	pid := started.cmd.Process.Pid
	if sid, err := unix.Getsid(pid); err != nil || sid != pid {
		t.Fatalf("desktop session = %d, %v; want its own session %d", sid, err, pid)
	}
	select {
	case exit := <-started.Exited():
		t.Fatalf("reachable desktop exited: %v", exit)
	default:
	}
}

func TestARunningAppIsReachedWithoutStartingAnother(t *testing.T) {
	fake := newFakeDesktop(t, "listen")
	listener, err := net.Listen("unix", fake.socket)
	testutil.FailErr(t, "net.Listen failed", err)
	defer func() { _ = listener.Close() }()
	go func() {
		if conn, err := listener.Accept(); err == nil {
			_ = conn.Close()
		}
	}()
	conn, err := dialOrStartAt(context.Background(), fake.socket, func(context.Context) (appStart, error) {
		t.Fatal("started a second app while one was reachable")
		return nil, nil
	}, time.Second, 20*time.Millisecond)
	testutil.FailErr(t, "dialOrStartAt failed", err)
	_ = conn.Close()
}

func TestADesktopThatExitsEarlyReportsItsStatus(t *testing.T) {
	_, _, err := newFakeDesktop(t, "fail").start(context.Background(), 20*time.Second)
	if err == nil || !strings.Contains(err.Error(), "exited before it was ready") || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("early exit = %v", err)
	}
}

func TestATimedOutStartStopsOnlyTheSessionItStarted(t *testing.T) {
	bystander := exec.Command("sleep", "60")
	if err := bystander.Start(); err != nil {
		testutil.FailErr(t, "bystander.Start failed", err)
	}
	defer func() { _ = bystander.Process.Kill(); _ = bystander.Wait() }()
	fake := newFakeDesktop(t, "family")
	_, started, err := fake.start(context.Background(), 500*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not finish starting") {
		t.Fatalf("timeout = %v", err)
	}
	groupGone(t, started.cmd.Process.Pid)
	raw, err := os.ReadFile(filepath.Join(fake.base, "child.pid"))
	testutil.FailErr(t, "read file", err)
	child, _ := strconv.Atoi(string(raw))
	if err := syscall.Kill(child, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("launched descendant %d survived: %v", child, err)
	}
	if err := syscall.Kill(bystander.Process.Pid, 0); err != nil {
		t.Fatalf("an unrelated process was stopped: %v", err)
	}
}

func TestCancellationKillsAStartThatIgnoresTermination(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := newFakeDesktop(t, "stubborn")
	time.AfterFunc(300*time.Millisecond, cancel)
	_, started, err := fake.start(ctx, 20*time.Second)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	groupGone(t, started.cmd.Process.Pid)
}

func TestNoGraphicalSessionStartsNothing(t *testing.T) {
	fake := newFakeDesktop(t, "listen")
	fake.launch.environment = []string{"PATH=/usr/bin", "DISPLAY="}
	if _, err := startDesktop(fake.launch); err == nil || !strings.Contains(err.Error(), "no graphical session") {
		t.Fatalf("headless start = %v", err)
	}
}

func TestOnlyAnInstallerControlledLauncherStarts(t *testing.T) {
	fake := newFakeDesktop(t, "listen")
	for name, change := range map[string]func(){
		"writable launcher": func() { _ = os.Chmod(fake.launch.path, 0o775) },
		"writable parent":   func() { _ = os.Chmod(filepath.Dir(fake.launch.path), 0o775) },
		"foreign owner":     func() { fake.launch.owner++ },
		"outside root":      func() { fake.launch.anchor = filepath.Join(fake.base, "other") },
		"not executable":    func() { _ = os.Chmod(fake.launch.path, 0o644) },
	} {
		t.Run(name, func(t *testing.T) {
			saved := fake.launch
			change()
			defer func() {
				fake.launch = saved
				_ = os.Chmod(saved.path, 0o755)
				_ = os.Chmod(filepath.Dir(saved.path), 0o755)
			}()
			if _, err := startDesktop(fake.launch); err == nil {
				t.Fatal("untrusted launcher started")
			}
		})
	}
	link := filepath.Join(filepath.Dir(fake.launch.path), "link")
	if err := os.Symlink(fake.launch.path, link); err != nil {
		testutil.FailErr(t, "os.Symlink failed", err)
	}
	if _, err := startDesktop(desktopLaunch{path: link, anchor: fake.base, owner: fake.launch.owner,
		environment: fake.launch.environment}); err == nil {
		t.Fatal("symlinked launcher started")
	}
	missing := fake.launch
	missing.path = filepath.Join(fake.base, "bin", "absent")
	if _, err := startDesktop(missing); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("missing launcher = %v", err)
	}
	if linuxDesktopLauncher != "/usr/libexec/painted-wolf-code/pw-launch" {
		t.Fatalf("installed launcher %q is not the desktop file's program", linuxDesktopLauncher)
	}
}

func TestStoppingAReapedDesktopNeverSignalsItsReleasedGroup(t *testing.T) {
	fake := newFakeDesktop(t, "fail")
	owned, err := startDesktop(fake.launch)
	testutil.FailErr(t, "start failed", err)
	started := owned.(*desktopStart)
	<-started.done
	if started.cmd.ProcessState == nil || !started.reaped {
		t.Fatal("desktop leader was not actually reaped")
	}
	bystander := exec.Command("sleep", "60")
	testutil.FailErr(t, "bystander start", bystander.Start())
	defer func() { _ = bystander.Process.Kill(); _ = bystander.Wait() }()
	started.signalGroup = func(osprocess.Incarnation, syscall.Signal) error {
		t.Error("Stop tried to signal a group after releasing its leader")
		return nil
	}
	started.Stop()
	if err := bystander.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unrelated process did not survive post-exit cleanup: %v", err)
	}
}

func TestStoppingKeepsTheExitedLeaderUntilItsStubbornChildIsKilled(t *testing.T) {
	fake := newFakeDesktop(t, "stubborn-family")
	owned, err := startDesktop(fake.launch)
	testutil.FailErr(t, "start failed", err)
	started := owned.(*desktopStart)
	t.Cleanup(started.Stop)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(fake.base, "child.ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stubborn descendant did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var signals []syscall.Signal
	started.signalGroup = func(leader osprocess.Incarnation, sig syscall.Signal) error {
		if _, err := os.Stat("/proc/" + strconv.Itoa(started.cmd.Process.Pid)); err != nil {
			t.Errorf("leader identity was released before group signal %v: %v", sig, err)
		}
		signals = append(signals, sig)
		return osprocess.SignalGroup(leader, sig)
	}
	started.Stop()
	if len(signals) != 2 || signals[0] != syscall.SIGTERM || signals[1] != syscall.SIGKILL {
		t.Fatalf("group settlement signals = %v", signals)
	}
	groupGone(t, started.cmd.Process.Pid)
}
