//go:build linux

package launchguard

// The protected launch fixture: a root-owned generation, manifest, policy and
// setuid bootstrap installed and inspected as root through sudo. Scenarios
// live in protected_launch_linux_test.go.

import (
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
	"strings"
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
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
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
	// The fixture's whole ancestry from "/" must be root-owned and neither
	// sticky nor group/world-writable.
	dir := filepath.Join("/opt", "pw-protected-launch-"+hex.EncodeToString(nonce[:]))
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
