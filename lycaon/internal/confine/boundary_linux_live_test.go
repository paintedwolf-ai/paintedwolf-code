//go:build linux

package confine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

// linuxProbeEnv selects what TestLinuxSandboxProbeProcess does when a confined
// launch runs this binary.
const linuxProbeEnv = "PW_CONFINE_LINUX_PROBE"

// linuxProbePathsEnv lists, one per line, the socket paths the unix probe tries.
const linuxProbePathsEnv = "PW_CONFINE_LINUX_PROBE_PATHS"

// errnoName prints a syscall error by its constant name, or "ok".
func errnoName(err error) string {
	var errno syscall.Errno
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &errno) && errno == 0:
		return "ok"
	case errors.Is(err, unix.EACCES):
		return "EACCES"
	case errors.Is(err, unix.ENOSYS):
		return "ENOSYS"
	case errors.Is(err, unix.EAFNOSUPPORT):
		return "EAFNOSUPPORT"
	}
	return err.Error()
}

// TestLinuxSandboxProbeProcess is the body confined commands run; it does
// nothing unless a test spawned it with a mode.
func TestLinuxSandboxProbeProcess(t *testing.T) {
	mode := os.Getenv(linuxProbeEnv)
	if mode == "" {
		return
	}
	os.Exit(runLinuxProbe(mode))
}

func runLinuxProbe(mode string) int {
	self, err := os.Executable()
	if err != nil {
		return 9
	}
	child := func(childMode string, setsid bool) *exec.Cmd {
		cmd := exec.Command(self, "-test.run=^TestLinuxSandboxProbeProcess$")
		cmd.Env = append(os.Environ(), linuxProbeEnv+"="+childMode)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: setsid}
		return cmd
	}
	switch mode {
	case "connect":
		_, err := net.DialTimeout("tcp", "1.1.1.1:80", 3*time.Second)
		fmt.Println("dial:", err)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fmt.Println("listen:", err)
			return 2
		}
		_ = ln.Close()
		fmt.Println("listen: ok")
		return 0
	case "ptrace":
		sleeper := child("sleep", false)
		if err := sleeper.Start(); err != nil {
			fmt.Println("start:", err)
			return 2
		}
		err := syscall.PtraceAttach(sleeper.Process.Pid)
		fmt.Println("ptrace:", err)
		_ = sleeper.Process.Kill()
		if err == nil {
			return 3
		}
		return 4
	case "unshare":
		flags, _ := strconv.Atoi(os.Getenv("PW_CONFINE_UNSHARE_FLAGS"))
		fmt.Println("unshare:", unix.Unshare(flags))
		return 4
	case "tiocsti", "tioclinux":
		// Take the inherited terminal as the controlling one, as a shell would,
		// so only the filter stands between the request and the terminal.
		if _, err := unix.Setsid(); err != nil {
			fmt.Println("setsid:", err)
			return 2
		}
		if err := unix.IoctlSetInt(0, unix.TIOCSCTTY, 0); err != nil {
			fmt.Println("controlling terminal:", err)
			return 2
		}
		input := byte('x')
		request := uint(unix.TIOCSTI)
		if mode == "tioclinux" {
			request = unix.TIOCLINUX
		}
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, 0, uintptr(request), uintptr(unsafe.Pointer(&input)))
		fmt.Println(mode+":", errno)
		return 4
	case "unix":
		for _, path := range strings.Split(os.Getenv(linuxProbePathsEnv), "\n") {
			for _, typ := range []int{unix.SOCK_STREAM, unix.SOCK_DGRAM, unix.SOCK_SEQPACKET} {
				fd, err := unix.Socket(unix.AF_UNIX, typ|unix.SOCK_CLOEXEC, 0)
				if err == nil {
					err = unix.Connect(fd, &unix.SockaddrUnix{Name: path})
					_ = unix.Close(fd)
				}
				fmt.Printf("unix %d %s: %v\n", typ, path, errnoName(err))
			}
		}
		return 0
	case "socketpair":
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			fmt.Println("socketpair:", err)
			return 2
		}
		_, _ = unix.Write(fds[0], []byte("p"))
		buf := make([]byte, 1)
		n, err := unix.Read(fds[1], buf)
		fmt.Println("socketpair:", n, string(buf[:n]), err)
		_, err = unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
		fmt.Println("datagram socketpair:", errnoName(err))
		return 0
	case "floors":
		return floorsProbe()
	case "longcwd":
		return longcwdProbe()
	case "interrupted":
		return interruptedProbe()
	case "families":
		return familiesProbe()
	case "iouring":
		params := make([]byte, 120)
		_, _, errno := unix.Syscall(unix.SYS_IO_URING_SETUP, 1, uintptr(unsafe.Pointer(&params[0])), 0)
		fmt.Println("io_uring_setup:", errnoName(errno))
		return 0
	case "proxyget":
		return proxyGetProbe()
	case "tcpconnect", "unixconnect", "serve", "resolve":
		return networkProbe(mode)
	case "udpsend", "udpsendto", "udpserve":
		return datagramProbe(mode)
	case "orphan":
		daemon := child("sleep", true)
		if err := daemon.Start(); err != nil {
			fmt.Println("start:", err)
			return 2
		}
		fmt.Println("orphan:", daemon.Process.Pid)
		return 0
	case "sleep":
		time.Sleep(30 * time.Second)
		return 0
	case "identity":
		wd, _ := os.Getwd()
		fmt.Println("uid:", os.Getuid(), "gid:", os.Getgid(), "pid:", os.Getpid(), "cwd:", wd)
		return 0
	}
	return 9
}

// requireLinuxSandbox fails, rather than skips, when the host cannot confine:
// the capability report is the finding.
func requireLinuxSandbox(t *testing.T) string {
	t.Helper()
	caps, _ := confine.LinuxCapabilities()
	if !caps.Available() {
		report, _ := json.MarshalIndent(caps, "", "  ")
		t.Fatalf("linux confinement unavailable on this host:\n%s", report)
	}
	self, err := os.Executable()
	testutil.FailErr(t, "os.Executable", err)
	// Every environment a test builds afterwards names the coverage directory.
	helperBinary(t, self)
	return self
}

// helperBuild is the coverage build of the helper, made once per test binary.
var helperBuild struct {
	once sync.Once
	path string
	err  error
}

// helperBinaryEnv names, for a test re-executed in another namespace, the
// helper its parent already chose.
const helperBinaryEnv = "PW_CONFINE_TEST_HELPER"

// childCoverDir is where a coverage run collects children's counters; empty in
// every other run.
func childCoverDir() string { return os.Getenv("PW_GO_CHILD_COVERDIR") }

// helperBinary is what confined launches run as their helper: the test binary
// itself, or in a coverage run a coverage build of the helper alone, since a
// test binary records nothing it runs as a child of itself.
func helperBinary(t *testing.T, self string) string {
	t.Helper()
	if built := os.Getenv(helperBinaryEnv); built != "" {
		return built
	}
	dir := childCoverDir()
	if dir == "" {
		return self
	}
	helperBuild.once.Do(func() {
		// Children inherit it; go test's own directory is never merged.
		helperBuild.err = os.Setenv("GOCOVERDIR", dir) //nolint:usetesting // Process-wide: every later test's helper shares the one build.
		helperBuild.path = filepath.Join(os.TempDir(), "pw-confine-helper-"+strconv.Itoa(os.Getpid()))
		build := exec.Command("go", "build", "-cover", "-tags", "confinehelpermain",
			"-ldflags=-X=main.coverageDirectoryHex="+fmt.Sprintf("%x", dir),
			"-coverpkg=github.com/lycaon/lycaon/internal/confine/...", "-o", helperBuild.path, "./testdata/helpermain")
		if out, err := build.CombinedOutput(); err != nil && helperBuild.err == nil {
			helperBuild.err = fmt.Errorf("build coverage helper: %w: %s", err, out)
		}
	})
	testutil.FailErr(t, "coverage helper", helperBuild.err)
	return helperBuild.path
}

func denyConfinement(roots ...string) confine.Confinement {
	return confine.Confinement{Roots: sandboxRoots(roots...), Network: confine.NetworkDeny, EgressOff: confine.EgressOffOverride}
}

// sandboxRoots adds the directory a coverage run collects child counters in,
// so a confined helper can record what it ran.
func sandboxRoots(roots ...string) []string {
	if dir := childCoverDir(); dir != "" {
		return append(roots, dir)
	}
	return roots
}

// confinedRun runs name under c and returns its wait status and combined output.
func linuxConfinedRun(t *testing.T, self string, c confine.Confinement, env []string, name string, args ...string) (syscall.WaitStatus, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd, cleanup, err := confine.Command(ctx, helperBinary(t, self), name, args, c)
	testutil.FailErr(t, "confine.Command", err)
	defer cleanup()
	if env != nil {
		cmd.Env = env
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	_ = cmd.Run()
	if cmd.ProcessState == nil {
		t.Fatalf("%s did not start: %s", name, out.String())
	}
	return cmd.ProcessState.Sys().(syscall.WaitStatus), out.String()
}

func probeEnv(mode string) []string {
	return append(os.Environ(), linuxProbeEnv+"="+mode)
}

const probeTestFlag = "-test.run=^TestLinuxSandboxProbeProcess$"

func TestLinuxCapabilitiesReportAnAppliedBoundary(t *testing.T) {
	requireLinuxSandbox(t)
	caps, ok := confine.LinuxCapabilities()
	if !ok || !caps.Applied || !caps.UserNamespaces || caps.LandlockABI < linuxsandbox.LandlockABIFloor || !caps.Seccomp || !caps.Pidfd {
		t.Fatalf("capabilities: %+v", caps)
	}
	if !confine.Available() {
		t.Fatal("Available must follow the probe")
	}
}

func TestLinuxWritesStayInsideWriteRoots(t *testing.T) {
	self := requireLinuxSandbox(t)
	proj := t.TempDir()
	// The account's real home lies outside every write root; $HOME may not,
	// since the test harness relocates it under the temporary root.
	account, err := user.Current()
	testutil.FailErr(t, "current user", err)
	outside, err := os.MkdirTemp(account.HomeDir, ".pwc-confine-test-") //nolint:usetesting // Must lie outside every write root.
	testutil.FailErr(t, "outside dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	c := denyConfinement(proj)

	status, out := linuxConfinedRun(t, self, c, nil, "/bin/sh", "-c", "echo inside > "+proj+"/ok && cat "+proj+"/ok")
	if status.ExitStatus() != 0 || !strings.Contains(out, "inside") {
		t.Fatalf("write inside root: exit %d: %s", status.ExitStatus(), out)
	}
	expectFloorTable(t, c, proj, []floorStep{{"create " + filepath.Join(outside, "no"), "EROFS"}})
	if _, err := os.Stat(filepath.Join(outside, "no")); !os.IsNotExist(err) {
		t.Fatalf("file outside root exists: %v", err)
	}
}

func TestLinuxReadRootsAreReadOnlyInsideWriteRoots(t *testing.T) {
	self := requireLinuxSandbox(t)
	proj := t.TempDir()
	vendor := filepath.Join(proj, "vendor")
	testutil.FailErr(t, "mkdir vendor", os.Mkdir(vendor, 0o755))
	testutil.FailErr(t, "seed vendor", os.WriteFile(filepath.Join(vendor, "lib.txt"), []byte("lib"), 0o644))
	c := denyConfinement(proj)
	c.ReadRoots = []string{vendor}
	status, out := linuxConfinedRun(t, self, c, nil, "/bin/sh", "-c", "cat "+vendor+"/lib.txt && echo x > "+vendor+"/new")
	if status.ExitStatus() == 0 || !strings.Contains(out, "lib") || !strings.Contains(out, "Read-only file system") {
		t.Fatalf("read root: exit %d: %s", status.ExitStatus(), out)
	}
}

func TestLinuxDeniedReadsAreHidden(t *testing.T) {
	self := requireLinuxSandbox(t)
	proj := t.TempDir()
	secret := filepath.Join(proj, "other-worker")
	const secretContent = "PAINTED_WOLF_CONFINE_DENIED_READ_SECRET_CONTENT"
	testutil.FailErr(t, "mkdir secret", os.Mkdir(secret, 0o755))
	testutil.FailErr(t, "seed secret", os.WriteFile(filepath.Join(secret, "token"), []byte(secretContent), 0o644))
	c := denyConfinement(proj)
	c.ReadDenyPaths = []string{secret}
	status, out := linuxConfinedRun(t, self, c, nil, "/bin/sh", "-c", "ls "+secret+" && cat "+secret+"/token && echo LEAK")
	if status.ExitStatus() == 0 || strings.Contains(out, secretContent) || strings.Contains(out, "LEAK") {
		t.Fatalf("denied read exposed content: exit %d: %s", status.ExitStatus(), out)
	}
}

func TestLinuxKeyMaterialFilesAreUnreadableEvenUnderAWriteRoot(t *testing.T) {
	self := requireLinuxSandbox(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	key := filepath.Join(home, ".ssh", "id_ed25519")
	testutil.FailErr(t, "mkdir .ssh", os.Mkdir(filepath.Dir(key), 0o700))
	testutil.FailErr(t, "seed key", os.WriteFile(key, []byte("PRIVATE"), 0o600))
	c := denyConfinement(t.TempDir())
	status, out := linuxConfinedRun(t, self, c, nil, "/bin/sh", "-c", "cat "+key)
	if status.ExitStatus() == 0 || strings.Contains(out, "PRIVATE") {
		t.Fatalf("key material readable: exit %d: %s", status.ExitStatus(), out)
	}
}

func TestLinuxControlPlaneIsHiddenButSessionScratchRestored(t *testing.T) {
	self := requireLinuxSandbox(t)
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	testutil.FailErr(t, "seed token", os.WriteFile(filepath.Join(cfg, "api.token"), []byte("bearer"), 0o600))
	scratch := filepath.Join(cfg, "sessions", "s1", "scratch")
	testutil.FailErr(t, "mkdir scratch", os.MkdirAll(scratch, 0o700))
	c := denyConfinement(t.TempDir())
	c.SessionScratchRoot = scratch
	status, out := linuxConfinedRun(t, self, c, nil, "/bin/sh", "-c",
		"cat "+cfg+"/api.token && echo LEAK; echo scratch > "+scratch+"/note && cat "+scratch+"/note")
	if strings.Contains(out, "bearer") || strings.Contains(out, "LEAK") {
		t.Fatalf("control plane readable: %s", out)
	}
	if status.ExitStatus() != 0 || !strings.Contains(out, "scratch") {
		t.Fatalf("session scratch not writable: exit %d: %s", status.ExitStatus(), out)
	}
}

func TestLinuxRenameAcrossDirectoriesWithinRoot(t *testing.T) {
	self := requireLinuxSandbox(t)
	proj := t.TempDir()
	testutil.FailErr(t, "mkdir a", os.Mkdir(filepath.Join(proj, "a"), 0o755))
	testutil.FailErr(t, "mkdir b", os.Mkdir(filepath.Join(proj, "b"), 0o755))
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(proj, "a", "f"), []byte("f"), 0o644))
	status, out := linuxConfinedRun(t, self, denyConfinement(proj), nil, "/bin/mv", filepath.Join(proj, "a", "f"), filepath.Join(proj, "b", "f"))
	if status.ExitStatus() != 0 {
		t.Fatalf("rename within root: exit %d: %s", status.ExitStatus(), out)
	}
	if _, err := os.Stat(filepath.Join(proj, "b", "f")); err != nil {
		t.Fatalf("renamed file missing: %v", err)
	}
}

func TestLinuxNetworkDenyLeavesOnlyLoopback(t *testing.T) {
	self := requireLinuxSandbox(t)
	status, out := linuxConfinedRun(t, self, denyConfinement(t.TempDir()), probeEnv("connect"), self, probeTestFlag)
	if status.ExitStatus() != 0 || !strings.Contains(out, "network is unreachable") || !strings.Contains(out, "listen: ok") {
		t.Fatalf("network deny: exit %d: %s", status.ExitStatus(), out)
	}
}

// Every unshare kills the process except filesystem state alone, which
// answers EPERM without running.
func TestLinuxEscapeSyscallsKillTheProcess(t *testing.T) {
	self := requireLinuxSandbox(t)
	for _, tc := range []struct {
		mode  string
		flags uint64
	}{{"ptrace", 0}, {"unshare", unix.CLONE_NEWNS}, {"unshare", unix.CLONE_FS | unix.CLONE_NEWUSER}, {"unshare", unix.CLONE_FS | 1<<32}, {"unshare", unix.CLONE_FS}} {
		status, out := linuxConfinedRun(t, self, denyConfinement(t.TempDir()), append(probeEnv(tc.mode), "PW_CONFINE_UNSHARE_FLAGS="+strconv.FormatUint(tc.flags, 10)), self, probeTestFlag)
		refused, killed := tc.flags == unix.CLONE_FS, status.Signaled() && status.Signal() == unix.SIGSYS
		if killed == refused || refused && !strings.Contains(out, "unshare: operation not permitted") {
			t.Fatalf("%s %#x: killed by SIGSYS %v, want %v: %v: %s", tc.mode, tc.flags, killed, !refused, status, out)
		}
	}
}

func TestLinuxTerminalInjectionKillsTheProcess(t *testing.T) {
	self := requireLinuxSandbox(t)
	for _, mode := range []string{"tiocsti", "tioclinux"} {
		master, slave := openPty(t)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		cmd, cleanup, err := confine.Command(ctx, helperBinary(t, self), self, []string{probeTestFlag}, denyConfinement(t.TempDir()))
		testutil.FailErr(t, "confine.Command", err)
		cmd.Env = probeEnv(mode)
		var out bytes.Buffer
		cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, &out, &out
		_ = cmd.Run()
		cleanup()
		cancel()
		if cmd.ProcessState == nil {
			t.Fatalf("%s: did not start: %s", mode, out.String())
		}
		status := cmd.ProcessState.Sys().(syscall.WaitStatus)
		if !status.Signaled() || status.Signal() != unix.SIGSYS {
			t.Fatalf("%s: want death by SIGSYS, got %v: %s", mode, status, out.String())
		}
		// Nothing reached the terminal's input queue.
		if queued, err := unix.IoctlGetInt(int(slave.Fd()), unix.TIOCINQ); err != nil || queued != 0 {
			t.Fatalf("%s: terminal input queue holds %d bytes (%v)", mode, queued, err)
		}
		_ = slave.Close()
		_ = master.Close()
	}
}

// A terminal interrupt reaches the confined program, not the helpers between
// it and the terminal, so an interactive interpreter survives Ctrl-C.
func TestLinuxTerminalInterruptReachesOnlyTheTarget(t *testing.T) {
	requireLinuxSandbox(t)
	self, err := os.Executable()
	testutil.FailErr(t, "test binary", err)
	python, err := exec.LookPath("python3")
	testutil.FailErr(t, "locate interactive interpreter", err)
	master, slave := openPty(t)
	defer master.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	root := t.TempDir()
	cmd, cleanup, err := confine.Command(ctx, helperBinary(t, self), python, []string{"-i", "-q", "-u"}, denyConfinement(root))
	testutil.FailErr(t, "confine.Command", err)
	defer cleanup()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid, cmd.SysProcAttr.Setctty, cmd.SysProcAttr.Ctty = true, true, 0
	testutil.FailErr(t, "start confined interpreter", cmd.Start())
	_ = slave.Close()
	terminal := watchTerminal(master)
	send := func(text string) {
		t.Helper()
		_, err := master.WriteString(text)
		testutil.FailErr(t, "write terminal input", err)
	}
	send("print('ready' + '-for-input')\n")
	terminal.await(t, "ready-for-input")
	send("\x03")
	terminal.await(t, "KeyboardInterrupt")
	created := filepath.Join(root, "after-interrupt")
	send(fmt.Sprintf("with open(%q, 'w') as output: output.write('created')\n\n", created))
	send("print('write' + '-survived')\n")
	terminal.await(t, "write-survived")
	contents, err := os.ReadFile(created)
	testutil.FailErr(t, "read file created after Ctrl-C", err)
	if string(contents) != "created" {
		t.Fatalf("file created after Ctrl-C = %q", contents)
	}
	send("raise SystemExit(7)\n")
	_ = cmd.Wait()
	if status, _ := cmd.ProcessState.Sys().(syscall.WaitStatus); status.Signaled() || status.ExitStatus() != 7 {
		t.Fatalf("interpreter exit after the interrupt: %v\n%q", status, terminal.text())
	}
}

func TestLinuxPerformerLossAnswersExitTimeCreate(t *testing.T) {
	self := requireLinuxSandbox(t)
	python, err := exec.LookPath("python3")
	testutil.FailErr(t, "locate interpreter", err)
	root := t.TempDir()
	script := fmt.Sprintf(`import atexit, errno, os, sys
def exit_create():
 print('exit-create-started', flush=True)
 try:
  fd = os.open(%q, os.O_WRONLY | os.O_CREAT, 0o600)
 except OSError as err:
  print('exit-create-answer:' + str(err.errno), flush=True)
 else:
  os.close(fd)
atexit.register(exit_create)
print('ready-for-exit', flush=True)
sys.stdin.readline()
raise SystemExit(7)
`, filepath.Join(root, "exit-create"))
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	confine.StartRefusalWatch(t.Context(), "")
	boundary := denyConfinement(root)
	lease, err := confine.BindAction(&boundary, confine.EgressCommand{SessionID: "performer-loss", ToolName: "command", CommandLine: python})
	testutil.FailErr(t, "bind performer-loss action", err)
	defer lease.Close(t.Context())
	cmd, cleanup, err := confine.Command(ctx, helperBinary(t, self), python, []string{"-c", script}, boundary)
	testutil.FailErr(t, "confine interpreter", err)
	defer cleanup()
	input, err := cmd.StdinPipe()
	testutil.FailErr(t, "interpreter input", err)
	defer input.Close()
	out := &terminalWatch{done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = out, out
	testutil.FailErr(t, "start interpreter", cmd.Start())
	go func() { _ = cmd.Wait(); close(out.done) }()
	defer func() { _ = cmd.Process.Kill(); <-out.done }()
	out.await(t, "ready-for-exit")
	performer := confinedDescendant(t, cmd.Process.Pid, linuxsandbox.PerformerFlag)
	target := confinedDescendant(t, cmd.Process.Pid, python)
	pidfd, err := unix.PidfdOpen(performer, 0)
	testutil.FailErr(t, "hold performer identity", err)
	defer unix.Close(pidfd)
	defer func() { _ = unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0) }()
	testutil.FailErr(t, "stop performer", unix.PidfdSendSignal(pidfd, unix.SIGSTOP, nil, 0))
	testutil.WaitFor(t, func() bool {
		if ctx.Err() != nil {
			t.Fatalf("performer never stopped: %s", out.text())
		}
		raw, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", performer))
		for _, line := range strings.Split(string(raw), "\n") {
			if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "State:" {
				return fields[1] == "T"
			}
		}
		return false
	})
	_, err = input.Write([]byte("exit\n"))
	testutil.FailErr(t, "start interpreter exit", err)
	out.await(t, "exit-create-started")
	testutil.WaitFor(t, func() bool {
		if ctx.Err() != nil {
			t.Fatalf("interpreter never entered exit create: %s", out.text())
		}
		raw, _ := os.ReadFile(fmt.Sprintf("/proc/%d/syscall", target))
		fields := strings.Fields(string(raw))
		return len(fields) > 0 && fields[0] == strconv.Itoa(unix.SYS_OPENAT)
	})
	testutil.FailErr(t, "end stopped performer", unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0))
	out.await(t, "exit-create-answer:"+strconv.Itoa(int(unix.EACCES)))
	<-out.done
	if status := cmd.ProcessState.Sys().(syscall.WaitStatus); !status.Exited() || status.ExitStatus() != 7 {
		t.Fatalf("interpreter exit after performer loss: %v\n%s", status, out.text())
	}
	if got := lease.SettledRefusals(t.Context()); got.Witness != confine.WitnessIncomplete {
		t.Fatalf("performer loss witness = %s, want incomplete", got.Witness)
	}
}

func confinedDescendant(t *testing.T, parent int, argument string) int {
	t.Helper()
	found := 0
	testutil.WaitFor(t, func() bool {
		queue := []int{parent}
		for len(queue) > 0 {
			pid := queue[0]
			queue = queue[1:]
			for _, child := range threadChildren(pid) {
				id, err := strconv.Atoi(child)
				if err != nil {
					continue
				}
				queue = append(queue, id)
				command, _ := os.ReadFile("/proc/" + child + "/cmdline")
				args := strings.Split(string(command), "\x00")
				if len(args) > 1 && (args[0] == argument || args[1] == argument) {
					found = id
					return true
				}
			}
		}
		return false
	})
	return found
}

// threadChildren lists the children of every thread of pid. A child belongs
// to the thread that forked it, and the helper forks from whichever thread
// its locked goroutine runs on, which is rarely the main thread.
func threadChildren(pid int) []string {
	tasks, _ := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	var children []string
	for _, task := range tasks {
		raw, _ := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/children", pid, task.Name()))
		children = append(children, strings.Fields(string(raw))...)
	}
	return children
}

// terminalWatch accumulates what a terminal's main side reads.
type terminalWatch struct {
	mu   sync.Mutex
	buf  strings.Builder
	done chan struct{}
}

func watchTerminal(master *os.File) *terminalWatch {
	w := &terminalWatch{done: make(chan struct{})}
	go func() {
		defer close(w.done)
		chunk := make([]byte, 4096)
		for {
			n, err := master.Read(chunk)
			w.mu.Lock()
			w.buf.Write(chunk[:n])
			w.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return w
}

func (w *terminalWatch) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *terminalWatch) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func (w *terminalWatch) await(t *testing.T, marker string) {
	t.Helper()
	for !strings.Contains(w.text(), marker) {
		select {
		case <-w.done:
			if !strings.Contains(w.text(), marker) {
				t.Fatalf("terminal closed before %q:\n%q", marker, w.text())
			}
			return
		default:
		}
		if t.Context().Err() != nil {
			t.Fatalf("terminal never showed %q:\n%q", marker, w.text())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTerminalWatchKeepsFinalOutputAfterClosing(t *testing.T) {
	read, write, err := os.Pipe()
	testutil.FailErr(t, "terminal watch pipe", err)
	defer read.Close()
	terminal := watchTerminal(read)
	_, err = write.WriteString("final-marker")
	testutil.FailErr(t, "write final output", err)
	testutil.FailErr(t, "close final output", write.Close())
	<-terminal.done
	terminal.await(t, "final-marker")
}

// openPty opens a pseudo-terminal pair that is no session's controlling terminal.
func openPty(t *testing.T) (master, slave *os.File) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	testutil.FailErr(t, "open /dev/ptmx", err)
	master = os.NewFile(uintptr(fd), "ptmx")
	testutil.FailErr(t, "unlock pty", unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0))
	n, err := unix.IoctlGetUint32(fd, unix.TIOCGPTN)
	testutil.FailErr(t, "pty number", err)
	slave, err = os.OpenFile("/dev/pts/"+strconv.FormatUint(uint64(n), 10), os.O_RDWR|unix.O_NOCTTY, 0)
	testutil.FailErr(t, "open pty slave", err)
	return master, slave
}

func TestLinuxUnixSocketsAreRefused(t *testing.T) {
	self := requireLinuxSandbox(t)
	// A socket the test serves outside every root; the test reaches it itself,
	// so the confined refusal is the boundary and not a missing listener.
	// The test's own temporary directory is too deep for a socket path.
	short, err := os.MkdirTemp("/tmp", "pw-unix-") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "socket directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	own := filepath.Join(short, "own.sock")
	ln, err := net.Listen("unix", own)
	testutil.FailErr(t, "listen", err)
	defer ln.Close()
	paths := []string{own, "/run/dbus/system_bus_socket", filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "bus")}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		conn, err := net.Dial("unix", p)
		testutil.FailErr(t, "unconfined dial "+p, err)
		_ = conn.Close()
	}
	env := append(probeEnv("unix"), linuxProbePathsEnv+"="+strings.Join(paths, "\n"))
	status, out := linuxConfinedRun(t, self, denyConfinement(t.TempDir()), env, self, probeTestFlag)
	if status.ExitStatus() != 0 {
		t.Fatalf("unix probe: exit %d: %s", status.ExitStatus(), out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	refused := 0
	for _, line := range lines {
		if !strings.HasPrefix(line, "unix ") {
			continue
		}
		if !strings.HasSuffix(line, ": EACCES") {
			t.Fatalf("unix socket not refused with EACCES: %s\n%s", line, out)
		}
		refused++
	}
	if refused != 3*len(paths) {
		t.Fatalf("refused %d attempts, want %d:\n%s", refused, 3*len(paths), out)
	}

	status, out = linuxConfinedRun(t, self, denyConfinement(t.TempDir()), probeEnv("socketpair"), self, probeTestFlag)
	if status.ExitStatus() != 0 || !strings.Contains(out, "socketpair: 1 p <nil>") || !strings.Contains(out, "datagram socketpair: EACCES") {
		t.Fatalf("socketpair: exit %d: %s", status.ExitStatus(), out)
	}
}

func longcwdProbe() int {
	// A working directory deeper than the kernel names in one page leaves
	// /proc/<pid>/cwd unreadable, so a relative socket path names nothing
	// the supervisor can decide.
	segment := strings.Repeat("d", 200)
	for depth := 0; depth < 24; depth++ {
		if err := unix.Mkdir(segment, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			fmt.Println("mkdir:", err)
			return 2
		}
		if err := unix.Chdir(segment); err != nil {
			fmt.Println("chdir:", err)
			return 2
		}
	}
	for _, op := range []string{"connect", "bind"} {
		fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			fmt.Println("socket:", err)
			return 2
		}
		sa := &unix.SockaddrUnix{Name: "relative.sock"}
		if op == "connect" {
			err = unix.Connect(fd, sa)
		} else {
			err = unix.Bind(fd, sa)
		}
		_ = unix.Close(fd)
		fmt.Printf("relative %s: %s\n", op, errnoName(err))
	}
	return 0
}

// A relative socket path whose working directory the supervisor cannot read
// is refused, never performed relative to the supervisor's own directory.
func TestLinuxUnresolvableRelativeSocketPathsAreRefused(t *testing.T) {
	self := requireLinuxSandbox(t)
	root := t.TempDir()
	c := denyConfinement(root)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd, cleanup, err := confine.Command(ctx, helperBinary(t, self), self, []string{probeTestFlag}, c)
	testutil.FailErr(t, "confine.Command", err)
	defer cleanup()
	cmd.Env = probeEnv("longcwd")
	cmd.Dir = root
	out, _ := cmd.CombinedOutput()
	if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 0 {
		t.Fatalf("longcwd probe: %v: %s", cmd.ProcessState, out)
	}
	for _, want := range []string{"relative connect: EACCES", "relative bind: EACCES"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("longcwd probe missing %q:\n%s", want, out)
		}
	}
}

func interruptedProbe() int {
	// Interrupt notified calls while they wait for the supervisor, the
	// way Go's preemption signal does in a busy toolchain, then confirm
	// the supervisor still answers.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				_ = unix.Kill(os.Getpid(), unix.SIGURG)
				time.Sleep(200 * time.Microsecond)
			}
		}
	}()
	refusedConnect := func() error {
		fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			return err
		}
		defer func() { _ = unix.Close(fd) }()
		return unix.Connect(fd, &unix.SockaddrUnix{Name: "/run/pw-interrupted-probe.sock"})
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				_ = refusedConnect()
			}
		}()
	}
	wg.Wait()
	close(stop)
	fmt.Println("after interruptions:", errnoName(refusedConnect()))
	return 0
}

// A notified call interrupted while it waits must not end supervision: later
// calls are still decided rather than failing with ENOSYS.
func TestLinuxSupervisorSurvivesInterruptedNotifications(t *testing.T) {
	self := requireLinuxSandbox(t)
	status, out := linuxConfinedRun(t, self, denyConfinement(t.TempDir()), probeEnv("interrupted"), self, probeTestFlag)
	if status.ExitStatus() != 0 || !strings.Contains(out, "after interruptions: EACCES") {
		t.Fatalf("interrupted probe: exit %d: %s", status.ExitStatus(), out)
	}
}

func familiesProbe() int {
	for _, family := range []struct {
		name   string
		domain int
		kind   int
		proto  int
	}{
		{"vsock", unix.AF_VSOCK, unix.SOCK_STREAM, 0},
		{"vsock datagram", unix.AF_VSOCK, unix.SOCK_DGRAM, 0},
		{"bluetooth", unix.AF_BLUETOOTH, unix.SOCK_STREAM, 0},
		{"netlink", unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_ROUTE},
	} {
		fd, err := unix.Socket(family.domain, family.kind|unix.SOCK_CLOEXEC, family.proto)
		if err == nil {
			_ = unix.Close(fd)
		}
		fmt.Printf("family %s: %s\n", family.name, errnoName(err))
	}
	return 0
}

// TestLinuxUnisolatedSocketFamiliesAreUnavailable verifies that AF_VSOCK reaches
// the hypervisor host from any network namespace, so no network mode creates
// it; netlink stays inside the sandbox's namespace.
func TestLinuxUnisolatedSocketFamiliesAreUnavailable(t *testing.T) {
	self := requireLinuxSandbox(t)
	for _, c := range []confine.Confinement{
		denyConfinement(t.TempDir()),
		{Roots: sandboxRoots(t.TempDir()), Network: confine.NetworkDirectIP, EgressOff: confine.EgressOffOverride},
	} {
		status, out := linuxConfinedRun(t, self, c, probeEnv("families"), self, probeTestFlag)
		if status.ExitStatus() != 0 {
			t.Fatalf("families probe (%v): exit %d: %s", c.Network, status.ExitStatus(), out)
		}
		for _, want := range []string{
			"family vsock: EAFNOSUPPORT", "family vsock datagram: EAFNOSUPPORT",
			"family bluetooth: EAFNOSUPPORT", "family netlink: ok",
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("families probe (%v) missing %q:\n%s", c.Network, want, out)
			}
		}
	}
}

func TestLinuxIoUringIsUnavailable(t *testing.T) {
	self := requireLinuxSandbox(t)
	status, out := linuxConfinedRun(t, self, denyConfinement(t.TempDir()), probeEnv("iouring"), self, probeTestFlag)
	if status.ExitStatus() != 0 || !strings.Contains(out, "io_uring_setup: ENOSYS") {
		t.Fatalf("io_uring: exit %d: %s", status.ExitStatus(), out)
	}
}

func TestLinuxDescendantsEndWithTheCommand(t *testing.T) {
	self := requireLinuxSandbox(t)
	c := denyConfinement(t.TempDir())
	lease, err := confine.BindAction(&c, confine.EgressCommand{ToolCallID: "orphan-test"})
	testutil.FailErr(t, "BindAction", err)
	defer lease.Close(context.Background())
	status, out := linuxConfinedRun(t, self, c, probeEnv("orphan"), self, probeTestFlag)
	if status.ExitStatus() != 0 || !strings.Contains(out, "orphan:") {
		t.Fatalf("orphan launcher: exit %d: %s", status.ExitStatus(), out)
	}
	survivors, err := lease.Survivors()
	testutil.FailErr(t, "Survivors", err)
	if len(survivors) != 0 {
		t.Fatalf("descendants outlived the command: %v", survivors)
	}
}

func TestLinuxExitStatusAndSignalsAreMirrored(t *testing.T) {
	self := requireLinuxSandbox(t)
	c := denyConfinement(t.TempDir())
	status, _ := linuxConfinedRun(t, self, c, nil, "/bin/sh", "-c", "exit 7")
	if status.ExitStatus() != 7 {
		t.Fatalf("exit code = %d, want 7", status.ExitStatus())
	}
	status, _ = linuxConfinedRun(t, self, c, nil, "/bin/sh", "-c", "kill -TERM $$")
	if !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatalf("signal death not mirrored: %v", status)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd, cleanup, err := confine.Command(ctx, helperBinary(t, self), "/bin/sleep", []string{"30"}, c)
	testutil.FailErr(t, "confine.Command", err)
	defer cleanup()
	testutil.FailErr(t, "start sleep", cmd.Start())
	time.Sleep(time.Second)
	testutil.FailErr(t, "signal helper", cmd.Process.Signal(syscall.SIGTERM))
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("forwarded SIGTERM did not end the command")
	}
	if ws := cmd.ProcessState.Sys().(syscall.WaitStatus); !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Fatalf("forwarded signal not mirrored: %v", ws)
	}
}

func TestLinuxIdentityAndWorkingDirectorySurvive(t *testing.T) {
	self := requireLinuxSandbox(t)
	proj := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd, cleanup, err := confine.Command(ctx, helperBinary(t, self), self, []string{probeTestFlag}, denyConfinement(proj))
	testutil.FailErr(t, "confine.Command", err)
	defer cleanup()
	cmd.Env = probeEnv("identity")
	cmd.Dir = proj
	out, err := cmd.CombinedOutput()
	testutil.FailErr(t, "identity probe: "+string(out), err)
	want := fmt.Sprintf("uid: %d gid: %d", os.Getuid(), os.Getgid())
	if !strings.Contains(string(out), want) || !strings.Contains(string(out), "cwd: "+proj) {
		t.Fatalf("identity: %s (want %s in %s)", out, want, proj)
	}
	if strings.Contains(string(out), "pid: 1\n") {
		t.Fatalf("the target must not be the namespace's first process: %s", out)
	}
}

func TestLinuxDevicesAreCurated(t *testing.T) {
	self := requireLinuxSandbox(t)
	status, out := linuxConfinedRun(t, self, denyConfinement(t.TempDir()), nil, "/bin/sh", "-c",
		"echo x > /dev/null && head -c 1 /dev/urandom > /dev/null && ls /dev")
	if status.ExitStatus() != 0 {
		t.Fatalf("device sinks: exit %d: %s", status.ExitStatus(), out)
	}
	for _, want := range []string{"null", "zero", "urandom", "tty", "pts", "shm", "fd"} {
		if !strings.Contains(out, want) {
			t.Fatalf("/dev lacks %s: %s", want, out)
		}
	}
	for _, absent := range []string{"sda", "nvme", "mem", "kmsg"} {
		if strings.Contains(out, absent) {
			t.Fatalf("/dev exposes %s: %s", absent, out)
		}
	}
}

// rawHelper runs the helper against a hand-built plan and returns its wait
// status and stderr.
func rawHelper(t *testing.T, self string, plan linuxsandbox.Plan, target ...string) (syscall.WaitStatus, string) {
	t.Helper()
	plan.Filesystem.WriteRoots = sandboxRoots(plan.Filesystem.WriteRoots...)
	raw, err := plan.Encode()
	testutil.FailErr(t, "encode plan", err)
	pr, pw, err := os.Pipe()
	testutil.FailErr(t, "pipe", err)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	args := append([]string{"__confine-exec", "--profile-fd", strconv.Itoa(3), "--"}, target...)
	cmd := exec.CommandContext(ctx, helperBinary(t, self), args...)
	cmd.ExtraFiles = []*os.File{pr}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write(raw)
	}()
	_ = cmd.Run()
	_ = pr.Close()
	if cmd.ProcessState == nil {
		t.Fatalf("helper did not start: %s", stderr.String())
	}
	return cmd.ProcessState.Sys().(syscall.WaitStatus), stderr.String()
}

func TestLinuxHelperRefusesWhenAttestationFails(t *testing.T) {
	self := requireLinuxSandbox(t)
	base := linuxsandbox.Plan{
		Version: linuxsandbox.PlanVersion,
		Attest:  linuxsandbox.Attest{HostNamespaces: linuxsandbox.HostNamespaces()},
		Network: linuxsandbox.Network{Mode: linuxsandbox.NetworkDeny},
	}
	for _, omit := range []string{linuxsandbox.OmitLandlock, linuxsandbox.OmitSeccomp} {
		plan := base
		plan.Attest.Omit = omit
		status, stderr := rawHelper(t, self, plan, "/bin/true")
		if status.ExitStatus() != confine.HelperFailureExit || !strings.Contains(stderr, "attestation failed") || !strings.Contains(stderr, omit) {
			t.Fatalf("omit %s: exit %d: %s", omit, status.ExitStatus(), stderr)
		}
	}
	probe := base
	probe.Probe = true
	if status, stderr := rawHelper(t, self, probe, self); status.ExitStatus() != 0 {
		t.Fatalf("probe plan: exit %d: %s", status.ExitStatus(), stderr)
	}
}

// A helper whose namespaces are not the engine's, as when the protected
// launch's broker starts it outside the engine's namespace, still relaunches
// itself into fresh namespaces rather than taking itself for the inner helper.
func TestLinuxHelperRelaunchesFromOutsideTheEnginesNamespaces(t *testing.T) {
	self := requireLinuxSandbox(t)
	plan := linuxsandbox.Plan{
		Version: linuxsandbox.PlanVersion,
		Attest:  linuxsandbox.Attest{HostNamespaces: map[string]string{"user": "user:[0]", "mnt": "mnt:[0]", "pid": "pid:[0]", "net": "net:[0]", "ipc": "ipc:[0]"}},
		Network: linuxsandbox.Network{Mode: linuxsandbox.NetworkDeny},
	}
	if status, stderr := rawHelper(t, self, plan, "/bin/true"); status.ExitStatus() != 0 {
		t.Fatalf("helper started outside the engine's namespaces: exit %d: %s", status.ExitStatus(), stderr)
	}
}

func TestLinuxHelperReportsTypedUnavailability(t *testing.T) {
	self := requireLinuxSandbox(t)
	unsupported := linuxsandbox.Plan{
		Version: linuxsandbox.PlanVersion,
		Attest:  linuxsandbox.Attest{HostNamespaces: linuxsandbox.HostNamespaces()},
		Network: linuxsandbox.Network{Mode: "tunnel"},
	}
	status, stderr := rawHelper(t, self, unsupported, "/bin/true")
	if status.ExitStatus() != confine.HelperFailureExit || !strings.Contains(stderr, linuxsandbox.UnsupportedNetworkMode) {
		t.Fatalf("unsupported plan: exit %d: %s", status.ExitStatus(), stderr)
	}
}
