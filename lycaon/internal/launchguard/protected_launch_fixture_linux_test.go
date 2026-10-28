//go:build linux

package launchguard

// The protected launch fixture: a root-owned generation, manifest, policy and
// setuid bootstrap installed and inspected as root through sudo. Scenarios
// live in protected_launch_linux_test.go.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/platformfloor"
	"github.com/lycaon/lycaon/internal/testutil"
)

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
	case "upgrade":
		err = stageFixtureGeneration(dir, "1.0.1", strings.Split(os.Getenv(fixtureBinsEnv), ":"), []byte("generation-1.0.1\n"))
	case "vault-server":
		err = runVaultServer(dir, os.Getenv(fixtureVaultBinEnv), os.Getenv(fixtureUIDEnv), os.Getenv(fixtureGIDEnv))
	case "seal-entry-parent":
		err = runSealEntryParent(dir, os.Getenv(fixtureUIDEnv), os.Getenv(fixtureGIDEnv))
	case "seal-entry-child":
		err = runSealEntryChild(dir)
	case "remove":
		err = os.RemoveAll(dir)
	case "lock-free":
		err = lockFree(filepath.Join(dir, "locks", os.Getenv(fixtureHash)+".lock"))
	case "tamper":
		digest := `"manifest_sha256":"` + os.Getenv(fixtureHash) + `"`
		tampered := strings.Replace(mustRead(filepath.Join(dir, "launch.json")), digest, `"manifest_sha256":"`+strings.Repeat("0", 64)+`"`, 1)
		err = os.WriteFile(filepath.Join(dir, "launch.json"), []byte(tampered), 0o644)
	case "module-paths":
		err = checkInstrumentedModulePaths(t, dir)
	case "module-stage":
		err = runFixtureModuleStage()
	case "module-agent":
		err = RunBrokerAgent()
	case "module-view":
		err = verifyModuleViewIsolation(dir, os.Getenv(fixtureDesktopPIDEnv))
	default:
		err = fmt.Errorf("unknown fixture mode %q", mode)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if testing.CoverMode() == "" && mode != "module-paths" {
		os.Exit(0)
	}
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
// fixtureLibraryRoot is the fixture policy's one library root. The fixture
// binaries are static, so the root only has to exist and be root-controlled.
const fixtureLibraryRoot = "/usr/lib"

func installFixture(dir string, binaries []string) error {
	if len(binaries) != 3 {
		return errors.New("fixture needs bootstrap, entry and desktop binaries")
	}
	install := filepath.Join(dir, "install")
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	for _, path := range []string{dir, install, filepath.Join(install, "bin"), filepath.Join(dir, "generations")} {
		if err := os.Mkdir(path, 0o755); err != nil {
			return err
		}
		if err := os.Chmod(path, 0o755); err != nil {
			return err
		}
	}
	if _, _, err := copyRootBinary(binaries[0], filepath.Join(dir, "pw-launch"), 0o4755, nil); err != nil {
		return err
	}
	return stageFixtureGeneration(dir, "1.0.0", binaries, bytes.Repeat([]byte("painted-wolf-code-desktop-image\n"), 512<<10))
}

func copyRootBinary(source, target string, mode uint32, extra []byte) (string, int64, error) {
	raw, err := os.ReadFile(source)
	if err != nil {
		return "", 0, err
	}
	raw = append(raw, extra...)
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

func stageFixtureGeneration(dir, version string, binaries []string, extra []byte) error {
	if len(binaries) != 3 {
		return errors.New("fixture needs bootstrap, entry and desktop binaries")
	}
	install := filepath.Join(dir, "install")
	staging := filepath.Join(dir, "staging")
	_ = os.RemoveAll(staging)
	for _, path := range []string{staging, filepath.Join(staging, "bin")} {
		if err := os.Mkdir(path, 0o755); err != nil {
			return err
		}
		if err := os.Chmod(path, 0o755); err != nil {
			return err
		}
	}
	var executables []pkgmanifest.Executable
	payload := []pkgmanifest.PayloadEntry{{Path: "bin", Kind: "directory", Mode: 0o755}}
	for _, role := range []struct {
		source string
		name   string
		extra  []byte
	}{
		{binaries[1], "pw-launch-entry", nil},
		{binaries[2], "painted-wolf-code", extra},
		{binaries[2], "pw", extra},
	} {
		sum, size, err := copyRootBinary(role.source, filepath.Join(staging, "bin", role.name), 0o755, role.extra)
		if err != nil {
			return err
		}
		executables = append(executables, pkgmanifest.Executable{Path: filepath.Join(install, "bin", role.name), SHA256: sum, Size: size})
		digest, length := sum, size
		payload = append(payload, pkgmanifest.PayloadEntry{Path: "bin/" + role.name, Kind: "file", Mode: 0o755, SHA256: &digest, Size: &length})
	}
	manifest := pkgmanifest.Manifest{
		FormatVersion: pkgmanifest.FormatVersion, Package: "painted-wolf-code", Version: version,
		Architecture: fixtureArchitecture(), InstallRoot: install, Executables: executables, Payload: payload,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if _, err := pkgmanifest.Read(strings.NewReader(string(raw))); err != nil {
		return err
	}
	for _, target := range []string{filepath.Join(staging, "manifest.json"), filepath.Join(install, "manifest.json")} {
		if err := os.WriteFile(target, raw, 0o644); err != nil {
			return err
		}
		if err := os.Chmod(target, 0o644); err != nil {
			return err
		}
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	generation := filepath.Join(dir, "generations", digest)
	if err := os.Rename(staging, generation); err != nil {
		return err
	}
	policy, err := json.Marshal(GenerationPolicy{Version: 1, GenerationRoot: generation, ManifestSHA256: digest, LogicalInstallRoot: install, LibraryRoots: []string{fixtureLibraryRoot}})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "launch.json"), policy, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "digest"), []byte(digest), 0o644)
}

func runVaultServer(dir, vaultBin, uidStr, gidStr string) error {
	uid, err := strconv.Atoi(uidStr)
	if err != nil || uid <= 0 {
		return fmt.Errorf("invalid vault server uid %q", uidStr)
	}
	gid, err := strconv.Atoi(gidStr)
	if err != nil || gid < 0 {
		return fmt.Errorf("invalid vault server gid %q", gidStr)
	}
	runDir := filepath.Join(dir, "run")
	stateDir := filepath.Join(dir, "vault-state")
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{{runDir, 0o755}, {stateDir, 0o700}} {
		if err := os.MkdirAll(item.path, item.mode); err != nil {
			return err
		}
		if err := os.Chmod(item.path, item.mode); err != nil {
			return err
		}
		if err := os.Chown(item.path, uid, gid); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "vault.pid"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return err
	}
	runtime.LockOSThread()
	if err := unix.Prctl(unix.PR_SET_KEEPCAPS, 1, 0, 0, 0); err != nil {
		return err
	}
	if err := unix.Setgroups([]int{gid}); err != nil {
		return err
	}
	if err := unix.Setresgid(gid, gid, gid); err != nil {
		return err
	}
	if err := unix.Setresuid(uid, uid, uid); err != nil {
		return err
	}
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	bit := uint32(1 << unix.CAP_SYS_PTRACE)
	data[0].Effective = bit
	data[0].Permitted = bit
	data[0].Inheritable = bit
	if err := unix.Capset(&hdr, &data[0]); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_RAISE, uintptr(unix.CAP_SYS_PTRACE), 0, 0); err != nil {
		return err
	}
	args := []string{
		vaultBin,
		"--socket", filepath.Join(runDir, "vault.sock"),
		"--state", stateDir,
		"--generations", filepath.Join(dir, "generations"),
		"--policy", filepath.Join(dir, "launch.json"),
		"--locks", filepath.Join(dir, "locks"),
	}
	return syscall.Exec(vaultBin, args, []string{"PATH=/usr/bin:/bin"})
}

func fixtureArchitecture() string {
	arch, _ := platformfloor.ELFArchForGoArch(runtime.GOARCH)
	return arch
}

// sudoFixture runs this test binary's fixture mode as root.
func sudoFixture(t *testing.T, mode, dir string, extra ...string) error {
	t.Helper()
	args := append([]string{"-n", "env", fixtureModeEnv + "=" + mode, fixtureDirEnv + "=" + dir}, extra...)
	if mode == "module-paths" {
		args = append(args, fixtureDeadlineEnv+"="+strconv.FormatInt(moduleFixtureDeadline(t).Add(-time.Second).UnixNano(), 10))
	}
	coverDir := os.Getenv("PW_GO_CHILD_COVERDIR")
	if testing.CoverMode() != "" && coverDir != "" {
		args = append(args, "GOCOVERDIR="+coverDir)
	}
	args = append(args, os.Args[0], "-test.run=^TestProtectedLaunchFixture$")
	if mode == "module-paths" {
		args = append(args, "-test.timeout="+time.Until(moduleFixtureDeadline(t)).String())
	}
	if testing.CoverMode() != "" && coverDir != "" {
		args = append(args, "-test.gocoverdir="+coverDir)
	}
	out, err := exec.Command("sudo", args...).CombinedOutput()
	if testing.CoverMode() != "" && coverDir != "" {
		_ = exec.Command("sudo", "-n", "chmod", "-R", "a+rX", coverDir).Run()
	}
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
	// The fixture's whole ancestry from "/" must be root-owned and neither
	// sticky nor group/world-writable.
	dir := filepath.Join("/var/lib", "pw-protected-launch-"+hex.EncodeToString(nonce[:]))
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
	return launchFixture{dir: dir, bootstrap: filepath.Join(dir, "pw-launch"), digest: string(digest), install: filepath.Join(dir, "install"), binaries: binaries}
}

// moduleFixtureCommand retains counters from fixture actors after identity changes.
func moduleFixtureCommand(t *testing.T, mode string, caller Identity) *exec.Cmd {
	t.Helper()
	args := []string{"-test.run=^TestProtectedLaunchFixture$"}
	deadline := moduleFixtureDeadline(t)
	args = append(args, "-test.timeout="+(time.Until(deadline)+time.Second).String())
	env := []string{fixtureModeEnv + "=" + mode, fixtureDeadlineEnv + "=" + strconv.FormatInt(deadline.UnixNano(), 10)}
	if parent := os.Getenv("GOCOVERDIR"); testing.CoverMode() != "" && parent != "" {
		dir, err := os.MkdirTemp(parent, "module-actor-") //nolint:usetesting // Child counters must stay in GOCOVERDIR.
		testutil.FailErr(t, "actor coverage directory", err)
		testutil.FailErr(t, "actor coverage identity", os.Chown(dir, int(caller.UID), int(caller.GID)))
		args = append(args, "-test.gocoverdir="+dir)
		env = append(env, "GOCOVERDIR="+dir)
		t.Cleanup(func() {
			testutil.FailErr(t, "collect actor coverage", collectChildCoverage(dir))
			testutil.FailErr(t, "remove actor coverage directory", os.Remove(dir))
		})
	}
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	cmd.Env = env
	return cmd
}

func moduleFixtureSelection(t *testing.T, dir string) (EntrySpec, *os.File) {
	t.Helper()
	uid, err := strconv.Atoi(os.Getenv(fixtureUIDEnv))
	testutil.FailErr(t, "fixture caller uid", err)
	gid, err := strconv.Atoi(os.Getenv(fixtureGIDEnv))
	testutil.FailErr(t, "fixture caller gid", err)
	if uid <= 0 || gid < 0 {
		t.Fatal("module fixture requires a non-root caller")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "launch.json"))
	testutil.FailErr(t, "fixture generation policy", err)
	policy, err := decodePolicy(raw)
	testutil.FailErr(t, "decode fixture policy", err)
	library := filepath.Join(dir, "actor-library-root")
	testutil.FailErr(t, "private declared library root", os.Mkdir(library, 0o755))
	testutil.FailErr(t, "baseline library module", os.WriteFile(filepath.Join(library, "baseline.so"), []byte("baseline-module"), 0o755))
	testutil.FailErr(t, "library baseline mount", unix.Mount(library, library, "", unix.MS_BIND|unix.MS_REC, ""))
	testutil.FailErr(t, "library mount propagation", unix.Mount("", library, "", unix.MS_SHARED, ""))
	policy.LibraryRoots = append(policy.LibraryRoots, library)
	desktop := filepath.Join(policy.GenerationRoot, "bin/painted-wolf-code")
	content, err := os.ReadFile(desktop)
	testutil.FailErr(t, "selected desktop bytes", err)
	digest := sha256.Sum256(content)
	home := filepath.Join(dir, "actor-home")
	testutil.FailErr(t, "actor home", os.Mkdir(home, 0o700))
	testutil.FailErr(t, "actor home identity", os.Chown(home, uid, gid))
	spec := EntrySpec{
		Version: 1, SupervisorPID: os.Getpid(), UID: uint32(uid), GID: uint32(gid), Groups: []uint32{uint32(gid)},
		Generation: policy, Image: pkgmanifest.Executable{Path: desktop, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(content))},
		Environment: []string{"HOME=" + home, "PATH=/usr/bin:/bin", "LANG=C.UTF-8"},
	}
	frame, err := sealEntrySpec(spec)
	testutil.FailErr(t, "sealed module selection", err)
	t.Cleanup(func() { _ = frame.Close() })
	return spec, frame
}

func checkInstrumentedModuleStage(t *testing.T, dir string, spec EntrySpec, frame *os.File) {
	t.Helper()
	open := func(path string) *os.File {
		file, err := os.Open(path)
		testutil.FailErr(t, "stage descriptor "+path, err)
		t.Cleanup(func() { _ = file.Close() })
		return file
	}
	generation := open(spec.Generation.GenerationRoot)
	desktop := open(spec.Image.Path)
	entry := open(filepath.Join(spec.Generation.GenerationRoot, "bin/pw-launch-entry"))
	origin := open("/proc/self/ns/user")
	lifetime, err := os.Create(filepath.Join(dir, "actor-lifetime"))
	testutil.FailErr(t, "generation lifetime", err)
	defer lifetime.Close()
	broker, peer, err := brokerPair()
	testutil.FailErr(t, "stage broker pair", err)
	defer broker.Close()
	defer peer.Close()
	input, inputW, err := os.Pipe()
	testutil.FailErr(t, "stage input", err)
	defer input.Close()
	defer inputW.Close()
	outputR, output, err := os.Pipe()
	testutil.FailErr(t, "stage output", err)
	defer outputR.Close()
	defer output.Close()
	cmd := moduleFixtureCommand(t, "module-stage", Identity{UID: spec.UID, GID: spec.GID})
	cmd.ExtraFiles = []*os.File{frame, generation, desktop, entry, origin, peer, lifetime, entry, broker, output}
	cmd.Stdin, cmd.Stderr = input, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNS}
	testutil.FailErr(t, "run instrumented stage", cmd.Run())
	_ = peer.Close()
	_ = output.Close()
	var facts protectedFacts
	testutil.FailErr(t, "desktop facts deadline", outputR.SetReadDeadline(moduleFixtureDeadline(t)))
	testutil.FailErr(t, "read stage desktop facts", json.NewDecoder(outputR).Decode(&facts))
	if facts.UIDs != [3]int{int(spec.UID), int(spec.UID), int(spec.UID)} || facts.ReadError != "" || facts.ModuleUser != "EPERM" || facts.ModuleLibrary != "ok" || facts.InstallWrite != "EROFS" || facts.Remount != "EPERM" {
		t.Fatalf("stage desktop boundary: %+v", facts)
	}
	if facts.Namespace.OwnerUID != 0 {
		t.Fatalf("stage desktop user namespace: %+v", facts.Namespace)
	}
	testutil.FailErr(t, "live stage watcher seal", verifyLiveWatcherProcess(facts.PID))
	_ = inputW.Close()
}

const fixtureDeadlineEnv = "PW_LAUNCH_FIXTURE_DEADLINE"

func moduleFixtureDeadline(t *testing.T) time.Time {
	t.Helper()
	if raw := os.Getenv(fixtureDeadlineEnv); raw != "" {
		deadline, err := strconv.ParseInt(raw, 10, 64)
		testutil.FailErr(t, "fixture deadline", err)
		return time.Unix(0, deadline)
	}
	if deadline, ok := t.Deadline(); ok {
		return deadline
	}
	return time.Now().Add(time.Minute)
}

func runFixtureModuleStage() error {
	output := os.NewFile(stageServerFD+1, "fixture-desktop-facts")
	defer output.Close()
	previous := os.Stdout
	os.Stdout = output
	defer func() { os.Stdout = previous }()
	return RunStage()
}

func checkModuleWatcherPollAuthority(t *testing.T, pid int) {
	t.Helper()
	proc := filepath.Join("/proc", strconv.Itoa(pid))
	descriptors, err := os.ReadDir(filepath.Join(proc, "fd"))
	testutil.FailErr(t, "watcher descriptors", err)
	mountinfo := map[string]bool{}
	var epolls []string
	for _, descriptor := range descriptors {
		target, err := os.Readlink(filepath.Join(proc, "fd", descriptor.Name()))
		testutil.FailErr(t, "watcher descriptor target", err)
		if strings.HasPrefix(target, proc+"/task/") && filepath.Base(target) == "mountinfo" {
			mountinfo[descriptor.Name()] = true
		}
		if target == "anon_inode:[eventpoll]" {
			epolls = append(epolls, descriptor.Name())
		}
	}
	if len(mountinfo) != 1 {
		t.Fatalf("watcher mount notification descriptors = %v, want one", mountinfo)
	}
	for _, epoll := range epolls {
		raw, err := os.ReadFile(filepath.Join(proc, "fdinfo", epoll))
		testutil.FailErr(t, "watcher epoll registrations", err)
		for line := range strings.SplitSeq(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "tfd:" && mountinfo[fields[1]] {
				t.Fatalf("mount notification descriptor %s also registered with epoll descriptor %s", fields[1], epoll)
			}
		}
	}
}
