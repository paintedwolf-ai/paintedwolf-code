//go:build linux

package launchguard

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/packagegeneration"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

const diagGlycinModeEnv = "PW_DIAG_GLYCIN_MODE"

// TestDiagGlycinLoaderExec reproduces the glycin-svg loader spawn with visible
// stderr to diagnose the amd64 platform launchguard failure. Temporary
// diagnostic; remove before the fix lands.
func TestDiagGlycinLoaderExec(t *testing.T) {
	mode := os.Getenv(diagGlycinModeEnv)
	if mode == "" {
		diagGlycinParent(t)
		return
	}
	diagGlycinChild(t, mode)
}

func diagGlycinLoaderPath(t *testing.T) string {
	loader := "/usr/libexec/glycin-loaders/2+/glycin-svg"
	if _, err := os.Stat(loader); err != nil {
		t.Skipf("glycin-svg loader absent on this host: %v", err)
	}
	return loader
}

func diagGlycinBwrapCommand(t *testing.T, loader string) *exec.Cmd {
	cache := t.TempDir()
	// Mirrors the exact bwrap command glycin builds on the failing runner
	// (minus --dbus-fd, which the loader reports as exit 2).
	command := exec.Command("bwrap",
		"--unshare-all", "--die-with-parent", "--chdir", "/",
		"--ro-bind", "/usr", "/usr",
		"--dev", "/dev",
		"--ro-bind-try", "/etc/ld.so.cache", "/etc/ld.so.cache",
		"--ro-bind-try", "/nix/store", "/nix/store",
		"--tmpfs", "/tmp-home",
		"--tmpfs", "/tmp-run",
		"--clearenv",
		"--setenv", "HOME", "/tmp-home",
		"--setenv", "XDG_RUNTIME_DIR", "/tmp-run",
		"--symlink", "/usr/lib", "/lib",
		"--symlink", "/usr/lib32", "/lib32",
		"--symlink", "/usr/lib64", "/lib64",
		"--ro-bind-try", "/etc/fonts/conf.d", "/etc/fonts/conf.d",
		"--ro-bind-try", "/etc/fonts/fonts.conf", "/etc/fonts/fonts.conf",
		"--ro-bind-try", "/var/cache/fontconfig", "/var/cache/fontconfig",
		"--bind-try", cache, cache,
		"--setenv", "XDG_CACHE_HOME", cache,
		loader,
	)
	command.Env = nil
	return command
}

func diagGlycinRun(t *testing.T, label string, command *exec.Cmd) int {
	output, err := command.CombinedOutput()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			t.Logf("%s: spawn failure: %v", label, err)
			return -1
		}
	}
	t.Logf("%s: exit %d output:\n%s", label, code, output)
	return code
}

func diagGlycinParent(t *testing.T) {
	loader := diagGlycinLoaderPath(t)
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skipf("bwrap absent: %v", err)
	}

	// Report which catalog executables resolve on this host: the fixture
	// staging silently skips paths that resolve to os.ErrNotExist.
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "packaging", "linux", "protected-launch.json"))
	testutil.FailErr(t, "read protected launch catalog", err)
	var catalog struct {
		Architectures map[string]struct {
			Multiarch string `json:"multiarch"`
		} `json:"architectures"`
		Families map[string]struct {
			Executables []string `json:"system_executables"`
		} `json:"families"`
	}
	testutil.FailErr(t, "parse protected launch catalog", json.Unmarshal(raw, &catalog))
	for _, template := range catalog.Families["deb"].Executables {
		path := strings.ReplaceAll(template, "{multiarch}", catalog.Architectures[runtime.GOARCH].Multiarch)
		resolved, err := packagegeneration.ResolveSystemExecutable(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			t.Logf("catalog %s: SKIPPED (ErrNotExist)", path)
		case err != nil:
			t.Logf("catalog %s: resolve error: %v", path, err)
		default:
			t.Logf("catalog %s: resolved %s", path, resolved)
		}
	}

	// Plain: no Landlock, no seccomp. The loader exits 2 on its own when the
	// exec succeeds (missing --dbus-fd); bwrap exits 1 with a die message when
	// sandbox setup or the exec fails.
	diagGlycinRun(t, "plain", diagGlycinBwrapCommand(t, loader))

	// Plain with an allow-all seccomp filter to exercise bwrap's --seccomp
	// application path.
	diagGlycinRun(t, "plain-seccomp", diagGlycinSeccompAllowAll(t, diagGlycinBwrapCommand(t, loader)))

	// Through a setuid exec hop: the real spawn reaches bwrap through the
	// setuid bootstrap and the desktop binary. If AppArmor drops the
	// inherited userns permission across the setuid hop, bwrap's user
	// namespace creation fails and bwrap exits 1.
	if user := os.Getenv("USER"); user != "" {
		hop := exec.Command("sudo", "-u", user, "bwrap")
		base := diagGlycinBwrapCommand(t, loader)
		hop.Args = append(hop.Args, base.Args[1:]...)
		hop.Env = nil
		diagGlycinRun(t, "sudo-hop", hop)
	}

	// Restricted: the protected execution closure with NNP on all threads
	// (matching SealEntry), isolating which grant shape allows the exec.
	// restricted-file grants bwrap by name, restricted-dir grants it through
	// the install-root subtree, restricted-none is the negative control.
	for _, mode := range []string{"restricted-file", "restricted-dir", "restricted-none", "restricted-direct"} {
		command := exec.Command(os.Args[0], "-test.run", "^TestDiagGlycinLoaderExec$", "-test.v")
		command.Env = append(os.Environ(), diagGlycinModeEnv+"="+mode)
		diagGlycinRun(t, mode, command)
	}

	// Namespace replication: the real desktop entry is cloned into a new user
	// namespace while inheriting the root-owned stage mount namespace. Run the
	// same bwrap command from that context to capture bwrap's die message.
	if uid := os.Getuid(); uid != 0 {
		stage := exec.Command("sudo", "-n", diagGlycinModeEnv+"=namespace-stage", os.Args[0], "-test.run", "^TestDiagGlycinLoaderExec$", "-test.v")
		stage.Env = nil
		diagGlycinRun(t, "namespace-stage", stage)
	}
	// A fresh user namespace nested directly under the initial one, with the
	// caller's own mount namespace, isolates namespace nesting from the
	// root-owned stage mount namespace. The clone must happen in the forked
	// child: Go threads share CLONE_FS, so an in-process unshare fails EINVAL.
	uid, gid := os.Getuid(), os.Getgid()
	useronly := exec.Command(os.Args[0], "-test.run", "^TestDiagGlycinLoaderExec$", "-test.v")
	useronly.Env = append(os.Environ(), diagGlycinModeEnv+"=namespace-useronly")
	useronly.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  unix.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: uid, Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: gid, Size: 1}},
		Credential:  &syscall.Credential{Uid: 0, Gid: 0},
	}
	diagGlycinRun(t, "namespace-useronly", useronly)

	// Label probe: exec a shell inside the same sandbox to reveal which
	// AppArmor profile the bwrap-exec'd process carries.
	label := diagGlycinBwrapCommand(t, loader)
	label.Args = label.Args[:len(label.Args)-1]
	label.Args = append(label.Args, "/bin/sh", "-c", "cat /proc/self/attr/current; id")
	diagGlycinRun(t, "label", label)

	// Environment facts: userns sysctls, bwrap apparmor attachments, and any
	// recent apparmor denials from the kernel log.
	for _, probe := range []struct{ label, command string }{
		{"restrict_unprivileged_userns", "cat /proc/sys/kernel/apparmor_restrict_unprivileged_userns"},
		{"unprivileged_userns_clone", "cat /proc/sys/kernel/unprivileged_userns_clone"},
		{"apparmor bwrap profiles", "ls /etc/apparmor.d/ | grep -i bwrap"},
		{"apparmor glycin profile", "cat /etc/apparmor.d/glycin.bwrap 2>/dev/null || echo absent"},
		{"apparmor bwrap-userns-restrict profile", "cat /etc/apparmor.d/bwrap-userns-restrict 2>/dev/null || echo absent"},
		{"self apparmor profile", "cat /proc/self/attr/current"},
		{"aa-status", "sudo aa-status 2>/dev/null | head -30 || echo unavailable"},
		{"apparmor denials", "sudo dmesg 2>/dev/null | grep -i 'apparmor.*denied' | tail -20"},
	} {
		output, err := exec.Command("bash", "-c", probe.command).CombinedOutput()
		t.Logf("%s: err=%v output:\n%s", probe.label, err, output)
	}
}

func diagGlycinChild(t *testing.T, mode string) {
	loader := diagGlycinLoaderPath(t)
	// The real SealEntry sets no_new_privs on every thread before
	// restrictExecution; AllThreadsSyscall6 requires uniform results, so a
	// single-thread prctl would crash the runtime instead.
	if _, _, errno := syscall.AllThreadsSyscall6(unix.SYS_PRCTL, unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0, 0); errno != 0 {
		t.Fatalf("no_new_privs: %v", errno)
	}
	if profile, err := os.ReadFile("/proc/self/attr/current"); err == nil {
		t.Logf("%s: apparmor profile: %s", mode, strings.TrimSpace(string(profile)))
	}
	switch mode {
	case "restricted-file", "restricted-dir", "restricted-none", "restricted-direct":
		diagGlycinRestricted(t, mode, loader)
	case "namespace-desktop":
		diagGlycinNamespaceDesktop(t, loader, mode)
	case "namespace-useronly":
		diagGlycinNamespaceUserOnly(t, loader, mode)
	default:
		t.Fatalf("unknown child mode %s", mode)
	}
}

// diagGlycinRestricted applies the execution closure with one grant shape and
// execs the target, isolating whether the closure allows granted execs.
func diagGlycinRestricted(t *testing.T, mode, loader string) {
	policy := GenerationPolicy{LogicalInstallRoot: "/opt"}
	switch mode {
	case "restricted-file":
		policy.SystemExecutables = []string{"/usr/bin/bwrap", loader}
	case "restricted-dir":
		policy.LogicalInstallRoot = "/usr/bin"
	case "restricted-direct":
		policy.SystemExecutables = []string{loader}
	}
	exe, err := os.Open("/proc/self/exe")
	testutil.FailErr(t, "open self as image", err)
	defer func() { _ = exe.Close() }()
	if err := restrictExecution(policy, exe); err != nil {
		t.Fatalf("restrict: %v", err)
	}
	target := "/usr/bin/bwrap"
	command := diagGlycinBwrapCommand(t, loader)
	if mode == "restricted-direct" {
		target = loader
		command = exec.Command(loader)
		command.Env = nil
	}
	t.Logf("%s: exec target %s", mode, target)
	diagGlycinRun(t, mode, command)
}

// diagGlycinNamespaceStage mirrors the protected bootstrap stage: root, in a
// private mount namespace, cloning the desktop entry into a new user
// namespace with full identity mappings as the login user.
func diagGlycinNamespaceStage(t *testing.T, loader string) {
	if os.Getuid() != 0 {
		t.Skip("namespace stage needs root")
	}
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		t.Fatalf("stage unshare mount namespace: %v", err)
	}
	uid, err := strconv.Atoi(os.Getenv("SUDO_UID"))
	testutil.FailErr(t, "read SUDO_UID", err)
	gid, err := strconv.Atoi(os.Getenv("SUDO_GID"))
	testutil.FailErr(t, "read SUDO_GID", err)
	desktop := exec.Command(os.Args[0], "-test.run", "^TestDiagGlycinLoaderExec$", "-test.v")
	desktop.Env = append(os.Environ(), diagGlycinModeEnv+"=namespace-desktop")
	desktop.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 unix.CLONE_NEWUSER,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: int(^uint32(0))}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: int(^uint32(0))}},
		GidMappingsEnableSetgroups: true,
		Credential:                 &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)},
	}
	diagGlycinRun(t, "namespace-desktop", desktop)
}

// diagGlycinNamespaceDesktop is the replicated desktop entry context: login
// user in a fresh user namespace, inheriting the root-owned mount namespace,
// under the protected execution closure.
func diagGlycinNamespaceDesktop(t *testing.T, loader, mode string) {
	diagGlycinNamespaceFacts(t, mode)
	exe, err := os.Open("/proc/self/exe")
	testutil.FailErr(t, "open self as image", err)
	defer func() { _ = exe.Close() }()
	policy := GenerationPolicy{LogicalInstallRoot: "/usr/bin", SystemExecutables: []string{"/usr/bin/bwrap", loader}}
	if err := restrictExecution(policy, exe); err != nil {
		t.Fatalf("restrict: %v", err)
	}
	diagGlycinRun(t, mode, diagGlycinBwrapCommand(t, loader))
	label := diagGlycinBwrapCommand(t, loader)
	label.Args = label.Args[:len(label.Args)-1]
	label.Args = append(label.Args, "/bin/sh", "-c", "cat /proc/self/attr/current; id")
	diagGlycinRun(t, mode+"-label", label)
}

// diagGlycinNamespaceUserOnly isolates user namespace nesting: the same bwrap
// command from a fresh user namespace nested under the initial one, with the
// caller's own mount namespace and no execution closure.
func diagGlycinNamespaceUserOnly(t *testing.T, loader, mode string) {
	diagGlycinNamespaceFacts(t, mode)
	diagGlycinRun(t, mode, diagGlycinBwrapCommand(t, loader))
	label := diagGlycinBwrapCommand(t, loader)
	label.Args = label.Args[:len(label.Args)-1]
	label.Args = append(label.Args, "/bin/sh", "-c", "cat /proc/self/attr/current; id")
	diagGlycinRun(t, mode+"-label", label)
}

func diagGlycinNamespaceFacts(t *testing.T, mode string) {
	for _, probe := range []struct{ label, path string }{
		{"uid_map", "/proc/self/uid_map"},
		{"ns_mnt", "/proc/self/ns/mnt"},
		{"ns_user", "/proc/self/ns/user"},
	} {
		if raw, err := os.ReadFile(probe.path); err == nil {
			t.Logf("%s %s: %s", mode, probe.label, strings.TrimSpace(string(raw)))
		}
	}
	if raw, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "CapEff:") || strings.HasPrefix(line, "NoNewPrivs:") {
				t.Logf("%s status: %s", mode, line)
			}
		}
	}
}

// diagGlycinSeccompAllowAll writes a one-instruction allow-all BPF filter and
// wires it as bwrap's --seccomp fd, exercising bwrap's seccomp application
// path without glycin's real filter.
func diagGlycinSeccompAllowAll(t *testing.T, command *exec.Cmd) *exec.Cmd {
	filterFile, err := os.CreateTemp(t.TempDir(), "diag-seccomp")
	testutil.FailErr(t, "create seccomp filter file", err)
	// BPF_RET|BPF_K with SECCOMP_RET_ALLOW: code 0x06, k 0x7fff0000.
	insn := []byte{0x06, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff, 0x7f}
	if _, err := filterFile.Write(insn); err != nil {
		t.Fatalf("write seccomp filter: %v", err)
	}
	if _, err := filterFile.Seek(0, 0); err != nil {
		t.Fatalf("seek seccomp filter: %v", err)
	}
	command.ExtraFiles = append(command.ExtraFiles, filterFile)
	seccompFD := strconv.Itoa(3 + len(command.ExtraFiles) - 1)
	loader := command.Args[len(command.Args)-1]
	command.Args = append(command.Args[:len(command.Args)-1], "--seccomp", seccompFD, loader)
	return command
}
