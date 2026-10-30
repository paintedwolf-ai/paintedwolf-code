//go:build linux

package launchguard

// The protected launch fixture: a root-owned generation, manifest, policy and
// setuid bootstrap installed and inspected as root through sudo. Scenarios
// live in protected_launch_linux_test.go.

import (
	"bytes"
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
	case "guardian-exec-failure":
		err = checkGuardianExecFailure(dir, os.Getenv(fixtureGuardianProbe), os.Getenv(fixtureUIDEnv), os.Getenv(fixtureGIDEnv), os.Getenv(fixtureFaultHOME), os.Getenv(fixtureHash))
	case "guardian-c":
		err = checkGuardianCTransition(dir, os.Getenv(fixtureGuardianProbe), os.Getenv(fixtureUIDEnv), os.Getenv(fixtureGIDEnv))
	case "nested-generation":
		err = nestFixtureGenerationStore(dir)
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
	case "fail-mount-role":
		err = failFixtureMountRole(dir, os.Getenv(fixtureDesktopPIDEnv), os.Getenv(fixtureFailedRoleEnv))
	case "setup-notice":
		err = checkBootstrapNotice(dir, os.Getenv(fixtureUIDEnv), os.Getenv(fixtureGIDEnv))
	case "mount-nested":
		err = checkLateNestedMounts(dir)
	case "mount-nested-probe":
		err = runNestedSingleProbe(dir)
	case "mount-pipeline":
		err = checkPrivateMountPipeline(dir, os.Getenv(fixtureUIDEnv), os.Getenv(fixtureGIDEnv))
	case "mount-prepare":
		err = checkInitialMountPreparation(dir, os.Getenv(fixtureUIDEnv), os.Getenv(fixtureGIDEnv))
	case "mount-source":
		err = checkMountSourceRole(dir, os.Getenv(fixtureUIDEnv), os.Getenv(fixtureGIDEnv), os.Getenv(fixtureSourcePathEnv))
	case "module-view":
		err = verifyModuleViewIsolation(dir, os.Getenv(fixtureDesktopPIDEnv))
	case "coverage-prepare":
		err = prepareLaunchCoverage(dir, os.Getenv(fixtureUIDEnv), os.Getenv(fixtureGIDEnv))
	case "coverage-observe":
		err = observeLaunchCoverage(t, dir)
	case "coverage-readable":
		err = makeLaunchCoverageReadable(dir, os.Getenv(fixtureUIDEnv))
	default:
		err = fmt.Errorf("unknown fixture mode %q", mode)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if mode == "mount-nested-probe" || testing.CoverMode() == "" {
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
	if len(binaries) != 4 {
		return errors.New("fixture needs bootstrap, entry, desktop and fixed transition binaries")
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
	if len(binaries) != 4 {
		return errors.New("fixture needs bootstrap, entry, desktop and fixed transition binaries")
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
		{binaries[3], "pw-mount-intake-transition", nil},
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
	coverDir := os.Getenv("PW_GO_CHILD_COVERDIR")
	if testing.CoverMode() != "" && coverDir != "" {
		args = append(args, "GOCOVERDIR="+coverDir)
	}
	args = append(args, os.Args[0], "-test.run=^TestProtectedLaunchFixture$")
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
	if mode == "module-view" || mode == "mount-nested" {
		t.Logf("mediated mount visibility observation: %s", out)
	}
	return nil
}

func goBuild(t *testing.T, output, pkg string, ldflags string, options ...string) {
	t.Helper()
	args := []string{"build", "-trimpath", "-o", output}
	args = append(args, options...)
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
func installLaunchFixture(t *testing.T, modes ...launchFixtureBuild) launchFixture {
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
	measured := len(modes) == 1 && modes[0] == measuredLaunchFixture && testing.CoverMode() != ""
	var options []string
	entryFlags := ""
	if measured {
		overlay, linked := launchCoverageOverlay(t, build, dir)
		options = []string{"-cover", "-covermode=atomic", "-coverpkg=" + module + ",github.com/lycaon/lycaon/cmd/pw-launch,github.com/lycaon/lycaon/cmd/pw-launch-entry", "-overlay=" + overlay, launchCoverageTool(t, overlay)}
		layout += " " + linked
		entryFlags = linked
	}
	binaries := []string{filepath.Join(build, "pw-launch"), filepath.Join(build, "pw-launch-entry"), filepath.Join(build, "painted-wolf-code"), filepath.Join(build, "pw-mount-intake-transition")}
	goBuild(t, binaries[0], "github.com/lycaon/lycaon/cmd/pw-launch", layout, options...)
	goBuild(t, binaries[1], "github.com/lycaon/lycaon/cmd/pw-launch-entry", entryFlags, options...)
	goBuild(t, binaries[2], "./testdata/protecteddesktop", "")
	buildMountTransition(t, binaries[3])
	for _, binary := range binaries {
		testutil.FailErr(t, "fixture binary readable", os.Chmod(binary, 0o755))
	}
	testutil.FailErr(t, "make build directory traversable", os.Chmod(build, 0o755))
	testutil.FailErr(t, "install fixture", sudoFixture(t, "install", dir, fixtureBinsEnv+"="+strings.Join(binaries, ":")))
	if measured {
		testutil.FailErr(t, "prepare owned native runtime collector", sudoFixture(t, "coverage-prepare", dir,
			fixtureUIDEnv+"="+strconv.Itoa(os.Getuid()), fixtureGIDEnv+"="+strconv.Itoa(os.Getgid())))
	}
	t.Cleanup(func() {
		if err := sudoFixture(t, "remove", dir); err != nil {
			t.Errorf("remove fixture: %v", err)
		}
	})
	digest, err := os.ReadFile(filepath.Join(dir, "digest"))
	testutil.FailErr(t, "fixture digest", err)
	return launchFixture{dir: dir, bootstrap: filepath.Join(dir, "pw-launch"), digest: string(digest), install: filepath.Join(dir, "install"), binaries: binaries, measured: measured}
}

func nestFixtureGenerationStore(dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, "launch.json"))
	if err != nil {
		return err
	}
	var policy GenerationPolicy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return err
	}
	source := filepath.Join(dir, "generations")
	if filepath.Dir(policy.GenerationRoot) != source {
		return errors.New("fixture generation is outside selected store")
	}
	target := filepath.Join(policy.LogicalInstallRoot, "generations")
	if err := os.Rename(source, target); err != nil {
		return err
	}
	policy.GenerationRoot = filepath.Join(target, filepath.Base(policy.GenerationRoot))
	raw, err = json.Marshal(policy)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "launch.json"), raw, 0644)
}
