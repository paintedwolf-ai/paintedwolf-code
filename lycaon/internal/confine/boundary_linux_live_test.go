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
		fmt.Println("unshare:", unix.Unshare(unix.CLONE_NEWNS))
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
		return 0
	case "iouring":
		params := make([]byte, 120)
		_, _, errno := unix.Syscall(unix.SYS_IO_URING_SETUP, 1, uintptr(unsafe.Pointer(&params[0])), 0)
		fmt.Println("io_uring_setup:", errnoName(errno))
		return 0
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
	return self
}

func denyConfinement(roots ...string) confine.Confinement {
	return confine.Confinement{Roots: roots, Network: confine.NetworkDeny, EgressOff: confine.EgressOffOverride}
}

// confinedRun runs name under c and returns its wait status and combined output.
func linuxConfinedRun(t *testing.T, self string, c confine.Confinement, env []string, name string, args ...string) (syscall.WaitStatus, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd, cleanup, err := confine.Command(ctx, self, name, args, c)
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
	outside, err := os.MkdirTemp(account.HomeDir, ".pwc-confine-test-")
	testutil.FailErr(t, "outside dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	c := denyConfinement(proj)

	status, out := linuxConfinedRun(t, self, c, nil, "/bin/sh", "-c", "echo inside > "+proj+"/ok && cat "+proj+"/ok")
	if status.ExitStatus() != 0 || !strings.Contains(out, "inside") {
		t.Fatalf("write inside root: exit %d: %s", status.ExitStatus(), out)
	}
	status, out = linuxConfinedRun(t, self, c, nil, "/bin/sh", "-c", "echo escape > "+outside+"/no")
	if status.ExitStatus() == 0 {
		t.Fatalf("write outside root succeeded: %s", out)
	}
	if !strings.Contains(out, "Permission denied") {
		t.Fatalf("write outside root must be refused by the kernel, got: %s", out)
	}
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
	testutil.FailErr(t, "mkdir secret", os.Mkdir(secret, 0o755))
	testutil.FailErr(t, "seed secret", os.WriteFile(filepath.Join(secret, "token"), []byte("s3"), 0o644))
	c := denyConfinement(proj)
	c.ReadDenyPaths = []string{secret}
	status, out := linuxConfinedRun(t, self, c, nil, "/bin/sh", "-c", "ls "+secret+" && cat "+secret+"/token && echo LEAK")
	if status.ExitStatus() == 0 || strings.Contains(out, "s3") || strings.Contains(out, "LEAK") {
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

func TestLinuxEscapeSyscallsKillTheProcess(t *testing.T) {
	self := requireLinuxSandbox(t)
	for _, mode := range []string{"ptrace", "unshare"} {
		status, out := linuxConfinedRun(t, self, denyConfinement(t.TempDir()), probeEnv(mode), self, probeTestFlag)
		if !status.Signaled() || status.Signal() != unix.SIGSYS {
			t.Fatalf("%s: want death by SIGSYS, got %v: %s", mode, status, out)
		}
	}
}

func TestLinuxTerminalInjectionKillsTheProcess(t *testing.T) {
	self := requireLinuxSandbox(t)
	for _, mode := range []string{"tiocsti", "tioclinux"} {
		master, slave := openPty(t)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		cmd, cleanup, err := confine.Command(ctx, self, self, []string{probeTestFlag}, denyConfinement(t.TempDir()))
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
	short, err := os.MkdirTemp("/tmp", "pw-unix-")
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
	if status.ExitStatus() != 0 || !strings.Contains(out, "socketpair: 1 p <nil>") {
		t.Fatalf("socketpair: exit %d: %s", status.ExitStatus(), out)
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
	cmd, cleanup, err := confine.Command(ctx, self, "/bin/sleep", []string{"30"}, c)
	testutil.FailErr(t, "confine.Command", err)
	defer cleanup()
	testutil.FailErr(t, "start sleep", cmd.Start())
	time.Sleep(time.Second)
	testutil.FailErr(t, "signal helper", cmd.Process.Signal(syscall.SIGTERM))
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
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
	cmd, cleanup, err := confine.Command(ctx, self, self, []string{probeTestFlag}, denyConfinement(proj))
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
	raw, err := plan.Encode()
	testutil.FailErr(t, "encode plan", err)
	pr, pw, err := os.Pipe()
	testutil.FailErr(t, "pipe", err)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	args := append([]string{"__confine-exec", "--profile-fd", strconv.Itoa(3), "--"}, target...)
	cmd := exec.CommandContext(ctx, self, args...)
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

func TestLinuxHelperReportsTypedUnavailability(t *testing.T) {
	self := requireLinuxSandbox(t)
	// Namespaces that match nothing make the outer helper believe it is already
	// inside, so it must fail on the first privileged step and say so.
	plan := linuxsandbox.Plan{
		Version: linuxsandbox.PlanVersion,
		Attest:  linuxsandbox.Attest{HostNamespaces: map[string]string{"user": "user:[0]", "mnt": "mnt:[0]", "pid": "pid:[0]", "net": "net:[0]"}},
		Network: linuxsandbox.Network{Mode: linuxsandbox.NetworkDeny},
	}
	status, stderr := rawHelper(t, self, plan, "/bin/true")
	reason, ok := linuxsandbox.ParseUnavailable(stderr)
	if status.ExitStatus() != confine.HelperUnavailableExit || !ok || reason.Code != linuxsandbox.UnavailableMount {
		t.Fatalf("exit %d, reason %+v (%v): %s", status.ExitStatus(), reason, ok, stderr)
	}
	unsupported := linuxsandbox.Plan{
		Version: linuxsandbox.PlanVersion,
		Attest:  linuxsandbox.Attest{HostNamespaces: linuxsandbox.HostNamespaces()},
		Network: linuxsandbox.Network{Mode: linuxsandbox.NetworkProxyOnly},
	}
	status, stderr = rawHelper(t, self, unsupported, "/bin/true")
	if status.ExitStatus() != confine.HelperFailureExit || !strings.Contains(stderr, linuxsandbox.UnsupportedNetworkMode) {
		t.Fatalf("unsupported plan: exit %d: %s", status.ExitStatus(), stderr)
	}
}
