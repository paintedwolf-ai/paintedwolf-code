//go:build linux

package launchguard

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestDiagGlycinLoaderBwrap reproduces the exact bwrap command the glycin
// client spawns for the SVG loader and captures the loader's stderr, which
// the client drops on PrematureExit. Temporary diagnostic; remove before the
// fix lands.
func TestDiagGlycinLoaderBwrap(t *testing.T) {
	loader := diagGlycinLoaderPath(t)
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skipf("bwrap absent: %v", err)
	}

	// Stage 1: the loader directly with no arguments. Expected: exit 2 with
	// "FD that facilitates the D-Bus connection not specified via --dbus-fd"
	// on stderr, proving the binary starts and env_logger reaches stderr.
	noargs := exec.Command(loader)
	noargs.Env = diagGlycinPayloadEnv(t, "")
	diagGlycinSpawnWait(t, "direct-noargs", noargs, 6*time.Second)

	// Stage 2: the loader directly with a held dbus peer. Expected: alive,
	// blocked in the zbus handshake. Exit 1 here means the loader itself
	// fails outside bwrap.
	direct := exec.Command(loader, "--dbus-fd", "3")
	peer, child := diagGlycinSocketPair(t)
	direct.Env = diagGlycinPayloadEnv(t, "")
	direct.ExtraFiles = []*os.File{child}
	diagGlycinSpawnWait(t, "direct-dbus-fd3", direct, 6*time.Second)
	_ = peer

	// Stage 3: the exact bwrap command from the failing run, with the dbus
	// socket at fd 21 and no seccomp filter. Expected: alive. Exit 1 here
	// reproduces the failure and the stderr names the cause.
	bwrapCmd := diagGlycinBwrapCommand(t, loader)
	peer3, child3 := diagGlycinSocketPair(t)
	bwrapCmd.ExtraFiles = diagGlycinFillFiles(t, 18)
	bwrapCmd.ExtraFiles = append(bwrapCmd.ExtraFiles, child3)
	bwrapCmd.Args = append(bwrapCmd.Args, "--dbus-fd", "21")
	diagGlycinSpawnWait(t, "bwrap-dbus-fd21-noseccomp", bwrapCmd, 8*time.Second)
	_ = peer3

	// Stage 4: the same command with an allow-all seccomp filter at fd 22,
	// exercising bwrap's seccomp application path without glycin's filter.
	bwrapSeccomp := diagGlycinBwrapCommand(t, loader)
	peer4, child4 := diagGlycinSocketPair(t)
	bwrapSeccomp.ExtraFiles = diagGlycinFillFiles(t, 18)
	bwrapSeccomp.ExtraFiles = append(bwrapSeccomp.ExtraFiles, child4)
	bwrapSeccomp.Args = append(bwrapSeccomp.Args, "--dbus-fd", "21")
	filter := diagGlycinSeccompAllowAll(t)
	bwrapSeccomp.ExtraFiles = append(bwrapSeccomp.ExtraFiles, filter)
	bwrapSeccomp.Args = append(bwrapSeccomp.Args, "--seccomp", strconv.Itoa(3+len(bwrapSeccomp.ExtraFiles)-1))
	diagGlycinSpawnWait(t, "bwrap-dbus-fd21-seccomp-allowall", bwrapSeccomp, 8*time.Second)
	_ = peer4
}

func diagGlycinLoaderPath(t *testing.T) string {
	loader := "/usr/libexec/glycin-loaders/2+/glycin-svg"
	if _, err := os.Stat(loader); err != nil {
		t.Skipf("glycin-svg loader absent on this host: %v", err)
	}
	return loader
}

// diagGlycinBwrapCommand mirrors the exact bwrap command glycin builds on the
// failing runner, including the fontconfig cache dir the client creates and
// binds onto itself.
func diagGlycinBwrapCommand(t *testing.T, loader string) *exec.Cmd {
	cache := filepath.Join(t.TempDir(), ".cache", "glycin", "usr", "libexec", "glycin-loaders", "2+", "glycin-svg")
	if err := os.MkdirAll(filepath.Join(cache, "fontconfig"), 0o755); err != nil {
		t.Fatalf("create glycin cache dir: %v", err)
	}
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

// diagGlycinPayloadEnv mirrors the payload environment after --clearenv plus
// glycin's --setenv entries.
func diagGlycinPayloadEnv(t *testing.T, cache string) []string {
	if cache == "" {
		cache = "/tmp-home"
	}
	return []string{
		"HOME=/tmp-home",
		"XDG_RUNTIME_DIR=/tmp-run",
		"XDG_CACHE_HOME=" + cache,
	}
}

// diagGlycinSocketPair returns a connected Unix socket pair; the child end is
// handed to the spawned process, the peer end stays open in the test so the
// loader's zbus handshake blocks instead of failing.
func diagGlycinSocketPair(t *testing.T) (*os.File, *os.File) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	peer := os.NewFile(uintptr(fds[0]), "diag-glycin-peer")
	child := os.NewFile(uintptr(fds[1]), "diag-glycin-child")
	if peer == nil || child == nil {
		t.Fatal("wrap socketpair fds")
	}
	t.Cleanup(func() {
		_ = peer.Close()
		_ = child.Close()
	})
	return peer, child
}

// diagGlycinFillFiles pads ExtraFiles with /dev/null so the next appended
// file lands on the wanted descriptor number (child fd = 3 + index).
func diagGlycinFillFiles(t *testing.T, count int) []*os.File {
	files := make([]*os.File, 0, count)
	for i := 0; i < count; i++ {
		null, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatalf("open devnull: %v", err)
		}
		t.Cleanup(func() { _ = null.Close() })
		files = append(files, null)
	}
	return files
}

// diagGlycinSeccompAllowAll writes a one-instruction allow-all BPF filter for
// bwrap's --seccomp fd, exercising bwrap's seccomp application path without
// glycin's real filter.
func diagGlycinSeccompAllowAll(t *testing.T) *os.File {
	filterFile, err := os.CreateTemp(t.TempDir(), "diag-seccomp")
	if err != nil {
		t.Fatalf("create seccomp filter file: %v", err)
	}
	// BPF_RET|BPF_K with SECCOMP_RET_ALLOW: code 0x06, k 0x7fff0000.
	insn := []byte{0x06, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff, 0x7f}
	if _, err := filterFile.Write(insn); err != nil {
		t.Fatalf("write seccomp filter: %v", err)
	}
	if _, err := filterFile.Seek(0, 0); err != nil {
		t.Fatalf("seek seccomp filter: %v", err)
	}
	t.Cleanup(func() {
		_ = filterFile.Close()
		_ = os.Remove(filterFile.Name())
	})
	return filterFile
}

// diagGlycinSpawnWait runs the command and reports either its exit status
// with captured stderr and stdout, or that it stayed alive for the hold
// duration (startup OK) before killing it.
func diagGlycinSpawnWait(t *testing.T, label string, command *exec.Cmd, hold time.Duration) {
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Logf("%s: spawn failure: %v", label, err)
		return
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		code := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else if err != nil {
			t.Logf("%s: wait error: %v", label, err)
		}
		t.Logf("%s: EXITED code=%d\nstderr:\n%s\nstdout:\n%s", label, code, stderr.String(), stdout.String())
	case <-time.After(hold):
		_ = command.Process.Kill()
		<-done
		t.Logf("%s: ALIVE after %v (startup OK); killed.\nstderr:\n%s\nstdout:\n%s", label, hold, stderr.String(), stdout.String())
	}
}
