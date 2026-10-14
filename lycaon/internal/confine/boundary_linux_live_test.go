//go:build linux

package confine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

// linuxProbeEnv selects what TestLinuxSandboxProbeProcess does when a confined
// launch runs this binary.
const linuxProbeEnv = "PW_CONFINE_LINUX_PROBE"

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
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "home", err)
	outside, err := os.MkdirTemp(home, ".pwc-confine-test-")
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
