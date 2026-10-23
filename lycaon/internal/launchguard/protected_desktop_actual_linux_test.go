//go:build linux && protected_desktop_artifact

package launchguard

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

const desktopArtifactEnv = "PW_LAUNCH_DESKTOP_ARTIFACT"
const desktopDriverEnv = "PW_LAUNCH_DESKTOP_DRIVER"

func TestProtectedLaunchActualDesktop(t *testing.T) {
	if os.Getuid() == 0 {
		t.Fatal("real desktop qualification requires an ordinary caller")
	}
	tree, err := exec.Command("git", "rev-parse", "HEAD^{tree}").Output()
	testutil.FailErr(t, "captured source tree", err)
	artifact, manifest, err := loadDesktopArtifact(os.Getenv(desktopArtifactEnv), strings.TrimSpace(string(tree)))
	testutil.FailErr(t, "real desktop artifact provenance and complete payload", err)
	if manifest.Architecture != fixtureArchitecture() {
		t.Fatal("desktop artifact architecture differs from this kernel lane")
	}
	var nonce [8]byte
	_, err = rand.Read(nonce[:])
	testutil.FailErr(t, "fixture identity", err)
	dir := filepath.Join("/opt", "pw-protected-desktop-"+hex.EncodeToString(nonce[:]))
	build := t.TempDir()
	bootstrap, entry := filepath.Join(build, "pw-launch"), filepath.Join(build, "pw-launch-entry")
	module := "github.com/lycaon/lycaon/internal/launchguard"
	flags := fmt.Sprintf("-X %s.policyPath=%s -X %s.lockDirectory=%s", module, filepath.Join(dir, "launch.json"), module, filepath.Join(dir, "locks"))
	goBuild(t, bootstrap, "github.com/lycaon/lycaon/cmd/pw-launch", flags)
	goBuild(t, entry, "github.com/lycaon/lycaon/cmd/pw-launch-entry", "")
	testutil.FailErr(t, "root-readable build directory", os.Chmod(build, 0o755))
	groups, err := os.Getgroups()
	testutil.FailErr(t, "caller groups", err)
	rawGroups, err := json.Marshal(groups)
	testutil.FailErr(t, "caller groups encoding", err)
	args := []string{"-n", "env", desktopDriverEnv + "=1", desktopArtifactEnv + "=" + os.Getenv(desktopArtifactEnv),
		"PW_DESKTOP_SOURCE_TREE=" + artifact.SourceTree, "PW_DESKTOP_FIXTURE=" + dir, "PW_DESKTOP_BOOTSTRAP=" + bootstrap, "PW_DESKTOP_ENTRY=" + entry,
		fixtureUIDEnv + "=" + strconv.Itoa(os.Getuid()), fixtureGIDEnv + "=" + strconv.Itoa(os.Getgid()), "PW_DESKTOP_GROUPS=" + string(rawGroups),
		os.Args[0], "-test.run=^TestProtectedLaunchActualDesktopFixture$", "-test.v"}
	cmd := exec.Command("sudo", args...)
	output, err := cmd.CombinedOutput()
	t.Logf("actual desktop runtime evidence:\n%s", output)
	testutil.FailErr(t, "real protected desktop with an unregistered package", err)
}

// The root driver owns only this fixture's mounts, processes and package view.
func TestProtectedLaunchActualDesktopFixture(t *testing.T) {
	if os.Getenv(desktopDriverEnv) == "" {
		return
	}
	if os.Geteuid() != 0 {
		t.Fatal("fixture driver requires root")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	uid, err := strconv.Atoi(os.Getenv(fixtureUIDEnv))
	testutil.FailErr(t, "caller uid", err)
	gid, err := strconv.Atoi(os.Getenv(fixtureGIDEnv))
	testutil.FailErr(t, "caller gid", err)
	if uid <= 0 || gid < 0 {
		t.Fatal("invalid ordinary caller")
	}
	var groups []uint32
	testutil.FailErr(t, "caller supplementary groups", json.Unmarshal([]byte(os.Getenv("PW_DESKTOP_GROUPS")), &groups))
	artifact, manifest, err := loadDesktopArtifact(os.Getenv(desktopArtifactEnv), os.Getenv("PW_DESKTOP_SOURCE_TREE"))
	testutil.FailErr(t, "revalidate artifact before privileged copy", err)
	dir := os.Getenv("PW_DESKTOP_FIXTURE")
	if filepath.Dir(dir) != "/opt" || !strings.HasPrefix(filepath.Base(dir), "pw-protected-desktop-") {
		t.Fatal("invalid owned fixture path")
	}
	testutil.FailErr(t, "reserve owned fixture", os.Mkdir(dir, 0o755))
	preserve := false
	defer func() {
		if !preserve {
			testutil.FailErr(t, "remove owned fixture", os.RemoveAll(dir))
		}
	}()
	testutil.FailErr(t, "owned outer subreaper", unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0))
	defer func() {
		if err := settleDesktopLineage(); err != nil {
			preserve = true
			t.Errorf("fixture lineage cleanup: %v (%s)", err, dir)
		}
	}()
	testutil.FailErr(t, "install exact artifact generation", installDesktopGeneration(dir, artifact, manifest, os.Getenv("PW_DESKTOP_BOOTSTRAP"), os.Getenv("PW_DESKTOP_ENTRY")))
	_, _, err = copyRootBinary(os.Args[0], filepath.Join(dir, "test-driver"), 0o755, nil)
	testutil.FailErr(t, "retain fixture driver before private temporary mount", err)
	// This namespace cannot change any host mount or reach external services.
	testutil.FailErr(t, "private fixture namespaces", unix.Unshare(unix.CLONE_NEWNS|unix.CLONE_NEWNET))
	testutil.FailErr(t, "private fixture propagation", unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""))
	for _, path := range []string{"/tmp", "/run"} {
		mode := "mode=0755"
		if path == "/tmp" {
			mode = "mode=1777"
		}
		testutil.FailErr(t, "private runtime mount", unix.Mount("tmpfs", path, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, mode))
	}
	for _, path := range []string{"/var/lib/dpkg", "/var/lib/rpm", "/usr/lib/sysimage/rpm"} {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			testutil.FailErr(t, "private empty package database", unix.Mount("tmpfs", path, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "mode=0755"))
		}
	}
	out, err := exec.Command("ip", "link", "set", "lo", "up").CombinedOutput()
	if err != nil {
		t.Fatalf("private loopback: %v %s", err, out)
	}
	verifyDesktopLineageCleanup(t)
	runActualDesktop(t, dir, uid, gid, groups, artifact)
}
