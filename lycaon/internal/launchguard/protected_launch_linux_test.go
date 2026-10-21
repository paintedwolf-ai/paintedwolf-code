//go:build linux

package launchguard

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/launchverify"
	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/platformfloor"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The protected launch suite installs a root-owned fixture generation and a
// setuid copy of the real bootstrap, built with the fixture's layout linked
// in, and launches it as the ordinary test user. Fixture installation and
// lock inspection run as root through sudo, which the Linux confinement leg
// provides; nothing else in the suite is privileged.

const (
	fixtureModeEnv = "PW_LAUNCH_FIXTURE_MODE"
	fixtureDirEnv  = "PW_LAUNCH_FIXTURE_DIR"
	fixtureBinsEnv = "PW_LAUNCH_FIXTURE_BINARIES"
	fixtureHash    = "PW_LAUNCH_FIXTURE_DIGEST"
)

// protectedFacts is the fixture desktop's report.
type protectedFacts struct {
	PID             int                         `json:"pid"`
	UIDs            [3]int                      `json:"uids"`
	GIDs            [3]int                      `json:"gids"`
	Groups          []int                       `json:"groups"`
	Namespace       launchverify.NamespaceFacts `json:"namespace"`
	Executable      string                      `json:"executable"`
	Arguments       []string                    `json:"arguments"`
	Environment     []string                    `json:"environment_keys"`
	BrokerPeerPID   int                         `json:"broker_peer_pid"`
	BrokerPeerUID   int                         `json:"broker_peer_uid"`
	BrokerType      int                         `json:"broker_type"`
	LifetimeRegular bool                        `json:"lifetime_regular"`
	OpenBeyond      []int                       `json:"open_beyond"`
	InstallWrite    string                      `json:"install_write"`
	Remount         string                      `json:"remount"`
	ReadError       string                      `json:"read_error,omitempty"`
}

// launchFixture is one installed fixture layout.
type launchFixture struct {
	dir       string
	bootstrap string
	digest    string
	install   string
}

func TestProtectedLaunchFixture(t *testing.T) {
	mode := os.Getenv(fixtureModeEnv)
	if mode == "" {
		return
	}
	dir := os.Getenv(fixtureDirEnv)
	var err error
	switch mode {
	case "install":
		err = installFixture(dir, strings.Split(os.Getenv(fixtureBinsEnv), ":"))
	case "remove":
		err = os.RemoveAll(dir)
	case "lock-free":
		err = lockFree(filepath.Join(dir, "locks", os.Getenv(fixtureHash)+".lock"))
	case "tamper":
		digest := `"manifest_sha256":"` + os.Getenv(fixtureHash) + `"`
		tampered := strings.Replace(mustRead(filepath.Join(dir, "launch.json")), digest, `"manifest_sha256":"`+strings.Repeat("0", 64)+`"`, 1)
		err = os.WriteFile(filepath.Join(dir, "launch.json"), []byte(tampered), 0o644)
	default:
		err = fmt.Errorf("unknown fixture mode %q", mode)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func mustRead(path string) string {
	raw, _ := os.ReadFile(path)
	return string(raw)
}

func lockFree(path string) error {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("generation lock still held: %w", err)
	}
	return nil
}

// installFixture runs as root: a root-owned generation holding the entry and
// the fixture desktop, its manifest, the policy, a logical install root and a
// setuid copy of the fixture-linked bootstrap.
func installFixture(dir string, binaries []string) error {
	if len(binaries) != 3 {
		return errors.New("fixture needs bootstrap, entry and desktop binaries")
	}
	install := filepath.Join(dir, "install")
	for _, path := range []string{dir, install, filepath.Join(install, "bin"), filepath.Join(dir, "staging"), filepath.Join(dir, "staging", "bin")} {
		if err := os.Mkdir(path, 0o755); err != nil {
			return err
		}
		if err := os.Chmod(path, 0o755); err != nil {
			return err
		}
	}
	copyRoot := func(source, target string, mode uint32) (string, int64, error) {
		raw, err := os.ReadFile(source)
		if err != nil {
			return "", 0, err
		}
		if err := os.WriteFile(target, raw, 0o700); err != nil {
			return "", 0, err
		}
		// Go's FileMode spells set-ID differently; apply the kernel mode.
		if err := unix.Chmod(target, uint32(mode)); err != nil {
			return "", 0, err
		}
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:]), int64(len(raw)), nil
	}
	if _, _, err := copyRoot(binaries[0], filepath.Join(dir, "pw-launch"), 0o4755); err != nil {
		return err
	}
	var executables []pkgmanifest.Executable
	var payload []pkgmanifest.PayloadEntry
	binDir := "bin"
	payload = append(payload, pkgmanifest.PayloadEntry{Path: binDir, Kind: "directory", Mode: 0o755})
	for _, role := range []struct{ source, name string }{{binaries[1], "pw-launch-entry"}, {binaries[2], "painted-wolf-code"}} {
		sum, size, err := copyRoot(role.source, filepath.Join(dir, "staging", "bin", role.name), 0o755)
		if err != nil {
			return err
		}
		executables = append(executables, pkgmanifest.Executable{Path: filepath.Join(install, "bin", role.name), SHA256: sum, Size: size})
		digest, length := sum, size
		payload = append(payload, pkgmanifest.PayloadEntry{Path: "bin/" + role.name, Kind: "file", Mode: 0o755, SHA256: &digest, Size: &length})
	}
	manifest := pkgmanifest.Manifest{
		FormatVersion: pkgmanifest.FormatVersion, Package: "painted-wolf-code", Version: "1.0.0",
		Architecture: fixtureArchitecture(), InstallRoot: install, Executables: executables, Payload: payload,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if _, err := pkgmanifest.Read(strings.NewReader(string(raw))); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "staging", "manifest.json"), raw, 0o644); err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	generation := filepath.Join(dir, "generations", digest)
	if err := os.Mkdir(filepath.Join(dir, "generations"), 0o755); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(dir, "staging"), generation); err != nil {
		return err
	}
	policy, err := json.Marshal(GenerationPolicy{Version: 1, GenerationRoot: generation, ManifestSHA256: digest, LogicalInstallRoot: install})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "launch.json"), policy, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "digest"), []byte(digest), 0o644)
}

func fixtureArchitecture() string {
	arch, _ := platformfloor.ELFArchForGoArch(runtime.GOARCH)
	return arch
}

// sudoFixture runs this test binary's fixture mode as root.
func sudoFixture(t *testing.T, mode, dir string, extra ...string) error {
	t.Helper()
	args := append([]string{"-n", "env", fixtureModeEnv + "=" + mode, fixtureDirEnv + "=" + dir}, extra...)
	args = append(args, os.Args[0], "-test.run=^TestProtectedLaunchFixture$")
	out, err := exec.Command("sudo", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s fixture: %w: %s", mode, err, out)
	}
	return nil
}

func goBuild(t *testing.T, output, pkg string, ldflags string) {
	t.Helper()
	args := []string{"build", "-trimpath", "-o", output}
	if ldflags != "" {
		args = append(args, "-ldflags", ldflags)
	}
	cmd := exec.Command("go", append(args, pkg)...)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, out)
	}
}

// installLaunchFixture builds the real bootstrap and entry with the fixture
// desktop and installs them as root.
func installLaunchFixture(t *testing.T) launchFixture {
	t.Helper()
	if out, err := exec.Command("sudo", "-n", "true").CombinedOutput(); err != nil {
		t.Fatalf("the protected launch suite needs passwordless sudo to install its root-owned fixture: %v %s", err, out)
	}
	var nonce [8]byte
	_, err := rand.Read(nonce[:])
	testutil.FailErr(t, "fixture nonce", err)
	dir := filepath.Join(os.TempDir(), "pw-protected-launch-"+hex.EncodeToString(nonce[:]))
	build := t.TempDir()
	module := "github.com/lycaon/lycaon/internal/launchguard"
	layout := fmt.Sprintf("-X %s.policyPath=%s -X %s.lockDirectory=%s", module, filepath.Join(dir, "launch.json"), module, filepath.Join(dir, "locks"))
	binaries := []string{filepath.Join(build, "pw-launch"), filepath.Join(build, "pw-launch-entry"), filepath.Join(build, "painted-wolf-code")}
	goBuild(t, binaries[0], "github.com/lycaon/lycaon/cmd/pw-launch", layout)
	goBuild(t, binaries[1], "github.com/lycaon/lycaon/cmd/pw-launch-entry", "")
	goBuild(t, binaries[2], "./testdata/protecteddesktop", "")
	for _, binary := range binaries {
		testutil.FailErr(t, "fixture binary readable", os.Chmod(binary, 0o755))
	}
	testutil.FailErr(t, "make build directory traversable", os.Chmod(build, 0o755))
	testutil.FailErr(t, "install fixture", sudoFixture(t, "install", dir, fixtureBinsEnv+"="+strings.Join(binaries, ":")))
	t.Cleanup(func() {
		if err := sudoFixture(t, "remove", dir); err != nil {
			t.Errorf("remove fixture: %v", err)
		}
	})
	digest, err := os.ReadFile(filepath.Join(dir, "digest"))
	testutil.FailErr(t, "fixture digest", err)
	return launchFixture{dir: dir, bootstrap: filepath.Join(dir, "pw-launch"), digest: string(digest), install: filepath.Join(dir, "install")}
}

// runningLaunch is a started bootstrap and the facts its desktop reported.
type runningLaunch struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	facts protectedFacts
}

func startLaunch(t *testing.T, f launchFixture, env []string, args ...string) runningLaunch {
	t.Helper()
	cmd := exec.Command(f.bootstrap, args...)
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	testutil.FailErr(t, "desktop input", err)
	stdout, err := cmd.StdoutPipe()
	testutil.FailErr(t, "desktop report", err)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	testutil.FailErr(t, "start bootstrap", cmd.Start())
	t.Cleanup(func() {
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
	})
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		lines <- line
	}()
	var line string
	select {
	case line = <-lines:
	case <-time.After(30 * time.Second):
		t.Fatalf("protected desktop never reported; bootstrap stderr: %s", stderr.String())
	}
	var facts protectedFacts
	if err := json.Unmarshal([]byte(line), &facts); err != nil {
		t.Fatalf("decode desktop report %q: %v; bootstrap stderr: %s", line, err, stderr.String())
	}
	return runningLaunch{cmd: cmd, stdin: stdin, facts: facts}
}

func refusedLaunch(t *testing.T, cmd *exec.Cmd, want string) {
	t.Helper()
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), want) {
		t.Fatalf("launch was not refused with %q: %v\n%s", want, err, out)
	}
}

func TestProtectedLaunchSealsTheDesktopInARootOwnedNamespace(t *testing.T) {
	f := installLaunchFixture(t)
	home := t.TempDir()
	env := []string{"HOME=" + home, "PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LD_PRELOAD=/tmp/planted.so", "GTK_MODULES=planted", "LYCAON_SANDBOX=off", "SSL_CERT_FILE=/tmp/planted.pem"}
	launch := startLaunch(t, f, env, "--fixture-argument")
	facts := launch.facts
	uid, gid := os.Getuid(), os.Getgid()
	if facts.ReadError != "" {
		t.Fatalf("desktop could not read its own namespace facts: %s", facts.ReadError)
	}
	if facts.UIDs != [3]int{uid, uid, uid} || facts.GIDs != [3]int{gid, gid, gid} {
		t.Fatalf("desktop credentials %v/%v, want the caller %d/%d on every credential", facts.UIDs, facts.GIDs, uid, gid)
	}
	groups, err := unix.Getgroups()
	testutil.FailErr(t, "caller groups", err)
	if !slices.Equal(facts.Groups, groups) {
		t.Fatalf("desktop groups %v, want %v", facts.Groups, groups)
	}
	if facts.Executable != filepath.Join(f.install, "bin/painted-wolf-code") {
		t.Fatalf("desktop executable %q is not the logical generation path", facts.Executable)
	}
	if len(facts.Arguments) != 2 || facts.Arguments[1] != "--fixture-argument" {
		t.Fatalf("desktop arguments %q", facts.Arguments)
	}
	for _, key := range facts.Environment {
		if !desktopEnvironmentKeys[key] && !strings.HasPrefix(key, "LC_") {
			t.Fatalf("environment key %q crossed the bootstrap: %v", key, facts.Environment)
		}
	}
	if !slices.Contains(facts.Environment, "HOME") || !slices.Contains(facts.Environment, "PATH") {
		t.Fatalf("session environment did not cross: %v", facts.Environment)
	}
	// The vault identity service's own attestation accepts the desktop.
	initial, err := launchverify.ReadNamespace("/proc", 0)
	testutil.FailErr(t, "read the initial namespace", err)
	if err := launchverify.ProtectedNamespacePair(initial, facts.Namespace, facts.Namespace); err != nil {
		t.Fatalf("service attestation refused the protected desktop: %v (%+v)", err, facts.Namespace)
	}
	// The supervisor is the bootstrap, back to the caller's identity.
	if facts.BrokerPeerPID != launch.cmd.Process.Pid || facts.BrokerPeerUID != uid || facts.BrokerType != unix.SOCK_SEQPACKET || !facts.LifetimeRegular {
		t.Fatalf("broker or lifetime descriptor not inherited from the supervisor: %+v", facts)
	}
	if len(facts.OpenBeyond) != 0 {
		t.Fatalf("bootstrap descriptors reached the desktop: %v", facts.OpenBeyond)
	}
	assertCallerCredentials(t, launch.cmd.Process.Pid)
	if facts.InstallWrite != "EROFS" || facts.Remount != "EPERM" {
		t.Fatalf("generation mount is not sealed: write %s, remount %s", facts.InstallWrite, facts.Remount)
	}
	// The desktop stays dumpable and traceable by anyone, so only the
	// root-owned namespace stands between it and a same-user reader.
	mem, err := os.Open(fmt.Sprintf("/proc/%d/mem", facts.PID))
	if err == nil {
		_ = mem.Close()
		t.Fatal("a same-user process outside the namespace opened the protected desktop's memory")
	}
	if !errors.Is(err, unix.EACCES) {
		t.Fatalf("opening protected memory: %v, want EACCES", err)
	}
	if err := sudoFixture(t, "lock-free", f.dir, fixtureHash+"="+f.digest); err == nil {
		t.Fatal("the generation lifetime lock was free while the desktop ran")
	}
	testutil.FailErr(t, "close desktop input", launch.stdin.Close())
	testutil.FailErr(t, "supervisor exit", launch.cmd.Wait())
	testutil.FailErr(t, "generation lock released after the tree ended", sudoFixture(t, "lock-free", f.dir, fixtureHash+"="+f.digest))
}

// assertCallerCredentials requires the running supervisor to hold only the
// caller's identity and no capability.
func assertCallerCredentials(t *testing.T, pid int) {
	t.Helper()
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	testutil.FailErr(t, "supervisor status", err)
	uid := fmt.Sprint(os.Getuid())
	for _, line := range strings.Split(string(status), "\n") {
		key, value, _ := strings.Cut(line, ":")
		fields := strings.Fields(value)
		switch key {
		case "Uid":
			if len(fields) != 4 || slices.ContainsFunc(fields, func(f string) bool { return f != uid }) {
				t.Fatalf("supervisor uids %v, want %s", fields, uid)
			}
		case "CapPrm", "CapEff", "CapAmb":
			if strings.Trim(value, " \t0") != "" {
				t.Fatalf("supervisor %s %s, want none", key, value)
			}
		}
	}
}

func TestProtectedLaunchRefusesWithoutTheSetuidTransition(t *testing.T) {
	f := installLaunchFixture(t)
	t.Run("ordinary copy", func(t *testing.T) {
		copied := filepath.Join(t.TempDir(), "pw-launch")
		raw, err := os.ReadFile(f.bootstrap)
		testutil.FailErr(t, "read bootstrap", err)
		testutil.FailErr(t, "copy bootstrap", os.WriteFile(copied, raw, 0o755))
		refusedLaunch(t, exec.Command(copied), "privileged bootstrap transition is unavailable")
	})
	t.Run("traced", func(t *testing.T) {
		cmd := exec.Command(f.bootstrap)
		cmd.SysProcAttr = &syscall.SysProcAttr{Ptrace: true}
		var out strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &out
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		testutil.FailErr(t, "start traced bootstrap", cmd.Start())
		var status syscall.WaitStatus
		_, err := syscall.Wait4(cmd.Process.Pid, &status, 0, nil)
		testutil.FailErr(t, "traced exec stop", err)
		testutil.FailErr(t, "detach", syscall.PtraceDetach(cmd.Process.Pid))
		err = cmd.Wait()
		if err == nil || !strings.Contains(out.String(), "privileged bootstrap transition is unavailable") {
			t.Fatalf("a traced bootstrap was not refused: %v\n%s", err, out.String())
		}
	})
	t.Run("no new privileges", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNoNewPrivilegesExec$")
		cmd.Env = append(os.Environ(), "PW_LAUNCH_NNP_TARGET="+f.bootstrap)
		refusedLaunch(t, cmd, "privileged bootstrap transition is unavailable")
	})
	t.Run("stage by a caller", func(t *testing.T) {
		refusedLaunch(t, exec.Command(f.bootstrap, StageArgument), "administrator identity")
	})
}

// TestNoNewPrivilegesExec executes its target with no_new_privs set.
func TestNoNewPrivilegesExec(t *testing.T) {
	target := os.Getenv("PW_LAUNCH_NNP_TARGET")
	if target == "" {
		return
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		os.Exit(3)
	}
	if err := syscall.Exec(target, []string{target}, []string{}); err != nil {
		os.Exit(4)
	}
}

func TestProtectedLaunchRefusesAGenerationThePolicyDoesNotName(t *testing.T) {
	f := installLaunchFixture(t)
	testutil.FailErr(t, "tamper policy", sudoFixture(t, "tamper", f.dir, fixtureHash+"="+f.digest))
	refusedLaunch(t, exec.Command(f.bootstrap), "generation manifest differs from installed policy")
}
