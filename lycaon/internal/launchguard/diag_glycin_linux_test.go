//go:build linux

package launchguard

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
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

	// Stage 5: strace the exact bwrap command so a startup exit 1 names its
	// failing syscall in the trace.
	if strace, err := exec.LookPath("strace"); err == nil {
		tracePath := filepath.Join(t.TempDir(), "startup.trace")
		bwrapTrace := diagGlycinBwrapCommand(t, loader)
		peer5, child5 := diagGlycinSocketPair(t)
		bwrapTrace.ExtraFiles = diagGlycinFillFiles(t, 18)
		bwrapTrace.ExtraFiles = append(bwrapTrace.ExtraFiles, child5)
		bwrapTrace.Args = append(bwrapTrace.Args, "--dbus-fd", "21")
		traced := exec.Command(strace, "-f", "-s", "300", "-e", "trace=%file,%process", "-o", tracePath)
		traced.Args = append(traced.Args, bwrapTrace.Args...)
		traced.Env = bwrapTrace.Env
		diagGlycinSpawnWait(t, "bwrap-strace-startup", traced, 8*time.Second)
		diagGlycinLogTrace(t, "startup", tracePath)
		_ = peer5
	} else {
		t.Logf("stage5: strace absent: %v", err)
	}

	// Stage 6: drive a real Init request over the loader's dbus socket so a
	// load-time failure reproduces with the loader's stderr captured. The
	// listener/dial pair gives godbus a unix transport with fd passing.
	diagGlycinDriveInit(t, loader)

	// Stage 7: replicate the protected namespace's NOEXEC mount posture
	// around the exact bwrap command. The protected launch sets
	// MOUNT_ATTR_NOEXEC on "/" recursively and restores exec only on policy
	// paths through file-typed projection mounts; glycin's single-mount
	// "--ro-bind /usr /usr" cannot carry those stacked projections into the
	// sandbox, so the loader's execve should fail EACCES there and bwrap
	// should exit 1.
	helper := filepath.Join(t.TempDir(), "diag-noexec-helper")
	if err := buildBinary(helper, "./testdata/diagnoexec", ""); err != nil {
		t.Fatalf("build diag noexec helper: %v", err)
	}
	for _, mode := range []string{"control", "noexec"} {
		child := exec.Command(helper, "-mode", mode, "-parent-uid", strconv.Itoa(os.Getuid()), "-parent-gid", strconv.Itoa(os.Getgid()))
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWUSER | unix.CLONE_NEWNS}
		output, err := child.CombinedOutput()
		if err != nil {
			t.Logf("stage7-%s: helper failed: %v\n%s", mode, err, output)
			continue
		}
		t.Logf("stage7-%s:\n%s", mode, output)
	}
}

// diagGlycinDriveInit spawns the loader under the exact failing bwrap command
// with RUST_LOG=debug, authenticates ANONYMOUS over its dbus socket, and sends
// a real Init request for the SVG GTK fails to load. A load-time failure then
// reproduces with the loader's stderr and exit status captured.
func diagGlycinDriveInit(t *testing.T, loader string) {
	sockDir, err := os.MkdirTemp("", "diag-glycin-sock-")
	if err != nil {
		t.Logf("stage6: temp dir: %v", err)
		return
	}
	sockPath := filepath.Join(sockDir, "dbus")
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Logf("stage6: listen: %v", err)
		_ = os.RemoveAll(sockDir)
		return
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.RemoveAll(sockDir)
	})
	client, err := dbus.Dial("unix:path="+sockPath, dbus.WithAuth(dbus.AuthAnonymous()))
	if err != nil {
		t.Logf("stage6: dial: %v", err)
		return
	}
	t.Cleanup(func() { _ = client.Close() })
	accepted, err := listener.Accept()
	if err != nil {
		t.Logf("stage6: accept: %v", err)
		return
	}
	unixConn, ok := accepted.(*net.UnixConn)
	if !ok {
		t.Logf("stage6: accepted conn is %T", accepted)
		return
	}
	childFile, err := unixConn.File()
	if err != nil {
		t.Logf("stage6: conn file: %v", err)
		return
	}
	t.Cleanup(func() { _ = childFile.Close() })

	bwrapLoad := diagGlycinBwrapCommand(t, loader)
	// RUST_LOG must precede the payload path: glycin's parser stops at the
	// first unknown argument after the payload, so options appended later
	// land in the payload's argv and the loader exits 2.
	bwrapLoad.Args = append(bwrapLoad.Args[:len(bwrapLoad.Args)-1], "--setenv", "RUST_LOG", "debug", loader)
	bwrapLoad.ExtraFiles = diagGlycinFillFiles(t, 18)
	bwrapLoad.ExtraFiles = append(bwrapLoad.ExtraFiles, childFile)
	bwrapLoad.Args = append(bwrapLoad.Args, "--dbus-fd", "21")

	var stdout6, stderr6 bytes.Buffer
	bwrapLoad.Stdout = &stdout6
	bwrapLoad.Stderr = &stderr6
	if err := bwrapLoad.Start(); err != nil {
		t.Logf("stage6: spawn failure: %v", err)
		return
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- bwrapLoad.Wait() }()

	authOK := false
	authCh := make(chan error, 1)
	go func() { authCh <- client.Auth([]dbus.Auth{dbus.AuthAnonymous()}) }()
	select {
	case err := <-authCh:
		if err == nil {
			authOK = true
			t.Logf("stage6: auth OK")
		} else {
			t.Logf("stage6: auth failed: %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Logf("stage6: auth timeout (loader likely died before handshake)")
	}

	if authOK {
		svgData := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8"><rect width="8" height="8" fill="#000"/></svg>`)
		if raw, err := os.ReadFile("/usr/share/icons/Adwaita/scalable/status/image-missing.svg"); err == nil {
			svgData = raw
		}
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Logf("stage6: pipe: %v", err)
		} else {
			if _, err := writer.Write(svgData); err != nil {
				t.Logf("stage6: svg write: %v", err)
			}
			_ = writer.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			call := client.Object("org.gnome.glycin", "/org/gnome/glycin").CallWithContext(ctx,
				"org.gnome.glycin.Loader.Init", 0, diagInitRequest{
					Fd:       dbus.UnixFD(int(reader.Fd())),
					MimeType: "image/svg+xml",
					Details:  map[string]dbus.Variant{},
				})
			cancel()
			if call.Err != nil {
				t.Logf("stage6: init call error: %v", call.Err)
			} else {
				t.Logf("stage6: init reply body: %v", call.Body)
			}
		}
	}

	select {
	case err := <-waitCh:
		code := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		}
		t.Logf("stage6: EXITED code=%d\nstderr:\n%s\nstdout:\n%s", code, stderr6.String(), stdout6.String())
	case <-time.After(6 * time.Second):
		_ = bwrapLoad.Process.Kill()
		<-waitCh
		t.Logf("stage6: ALIVE after init (load path OK); killed.\nstderr:\n%s\nstdout:\n%s", stderr6.String(), stdout6.String())
	}
}

// diagInitRequest mirrors glycin's InitRequest wire struct (h s a{sv}); the
// details dict is left empty so base_dir deserializes as None.
type diagInitRequest struct {
	Fd       dbus.UnixFD
	MimeType string
	Details  map[string]dbus.Variant
}

// diagGlycinLogTrace logs a strace capture: error-returning lines first, then
// the tail where a death would appear.
func diagGlycinLogTrace(t *testing.T, label string, path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Logf("%s trace: unreadable: %v", label, err)
		return
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	t.Logf("%s trace: %d lines total", label, len(lines))
	shown := 0
	for _, line := range lines {
		if shown >= 60 {
			break
		}
		if strings.Contains(line, "ENOENT") || strings.Contains(line, "EACCES") ||
			strings.Contains(line, "EPERM") || strings.Contains(line, "ELOOP") {
			t.Logf("%s-err| %s", label, line)
			shown++
		}
	}
	const tail = 120
	if len(lines) > tail {
		t.Logf("%s| ... tail ...", label)
		for _, line := range lines[len(lines)-tail:] {
			t.Logf("%s| %s", label, line)
		}
		return
	}
	for _, line := range lines {
		t.Logf("%s| %s", label, line)
	}
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
