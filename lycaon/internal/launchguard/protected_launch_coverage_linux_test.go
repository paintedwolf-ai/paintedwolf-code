//go:build linux

package launchguard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

type launchFixtureBuild uint8

const measuredLaunchFixture launchFixtureBuild = 1

// Only native fixture builds see these sources. The installed production
// images and the ordinary uninstrumented launch scenarios stay unchanged.
func launchCoverageOverlay(t *testing.T, build, directory string) (string, string) {
	t.Helper()
	working, err := os.Getwd()
	testutil.FailErr(t, "locate launchguard sources", err)
	original := filepath.Join(working, "guardian_exec_linux.go")
	raw, err := os.ReadFile(original)
	testutil.FailErr(t, "read actual guardian execution owner", err)
	const transition = "\treturn syscall.Exec(\"/proc/self/fd/\"+strconv.Itoa(guardianIncomingImage), []string{\"pw-mount-intake-transition\"}, []string{})"
	if strings.Count(string(raw), transition) != 1 {
		t.Fatal("guardian transition instrumentation no longer names one exact exec boundary")
	}
	instrumented := strings.Replace(string(raw), transition,
		strings.Replace(transition, "syscall.Exec(", "fixtureExecWithCoverage(syscall.Exec, ", 1), 1)
	if strings.Count(instrumented, "\n") != strings.Count(string(raw), "\n") {
		t.Fatal("fixture instrumentation changed production source lines")
	}
	backing := filepath.Join(build, "guardian_exec_linux.go")
	testutil.FailErr(t, "retain instrumented exec source", os.WriteFile(backing, []byte(instrumented), 0o600))
	initializer := filepath.Join(build, "fixture_coverage_linux.go")
	testutil.FailErr(t, "retain fixture coverage initializer", os.WriteFile(initializer, []byte(launchCoverageSource), 0o600))
	replacements := map[string]string{
		original: backing,
		filepath.Join(working, "fixture_coverage_linux.go"): initializer,
	}
	transfer := filepath.Join(working, "mount_transfer_linux.go")
	transferRaw, err := os.ReadFile(transfer)
	testutil.FailErr(t, "read actual typed mount transport", err)
	const send = "unix.SendmsgN(socket, packet, control, nil, unix.MSG_NOSIGNAL)"
	if strings.Count(string(transferRaw), send) != 1 {
		t.Fatal("mount progress instrumentation no longer names one exact protocol send")
	}
	transferSource := strings.Replace(string(transferRaw), send,
		"fixtureSendWithCoverage(unix.SendmsgN, socket, packet, control, nil, unix.MSG_NOSIGNAL, message.kind)", 1)
	if strings.Count(transferSource, "\n") != strings.Count(string(transferRaw), "\n") {
		t.Fatal("mount instrumentation changed production source lines")
	}
	transferBacking := filepath.Join(build, "mount_transfer_linux.go")
	testutil.FailErr(t, "retain instrumented mount transport", os.WriteFile(transferBacking, []byte(transferSource), 0o600))
	replacements[transfer] = transferBacking
	config, err := json.Marshal(map[string]any{"Replace": replacements})
	testutil.FailErr(t, "encode fixture overlay", err)
	overlay := filepath.Join(build, "coverage-overlay.json")
	testutil.FailErr(t, "write fixture overlay", os.WriteFile(overlay, config, 0o600))
	t.Logf("fixture exec source original=%x instrumented=%x initializer=%x", sha256.Sum256(raw),
		sha256.Sum256([]byte(instrumented)), sha256.Sum256([]byte(launchCoverageSource)))
	t.Logf("fixture mount source original=%x instrumented=%x", sha256.Sum256(transferRaw), sha256.Sum256([]byte(transferSource)))
	for _, command := range []string{"pw-launch", "pw-launch-entry"} {
		source, err := os.ReadFile(filepath.Join(working, "..", "..", "cmd", command, "main_linux.go"))
		testutil.FailErr(t, "read actual native role dispatcher", err)
		t.Logf("fixture dispatcher %s source=%x", command, sha256.Sum256(source))
	}
	linked := "-X github.com/lycaon/lycaon/internal/launchguard.fixtureCoverageDirectoryHex=" + hex.EncodeToString([]byte(filepath.Join(directory, "coverage")))
	return overlay, linked
}

const launchCoverageSource = `//go:build linux
package launchguard

import (
    "encoding/hex"
    "fmt"
    "os"
    "path/filepath"
    "runtime/coverage"
    "golang.org/x/sys/unix"
)

var fixtureCoverageDirectoryHex string
var fixtureCoverageDirectory = initializeFixtureCoverage()

// This dependency initializer precedes main's generated coverage initHook.
// The runtime retains its directory before the real main init scrubs Env.
func initializeFixtureCoverage() string {
    raw, err := hex.DecodeString(fixtureCoverageDirectoryHex)
    if err != nil || !filepath.IsAbs(string(raw)) {
        panic("invalid embedded native fixture collector")
    }
    if err := os.Setenv("GOCOVERDIR", string(raw)); err != nil {
        panic(err)
    }
    return string(raw)
}

func fixtureFlushCoverage() error {
    if err := coverage.WriteMetaDir(fixtureCoverageDirectory); err != nil {
        return fmt.Errorf("fixture stage metadata: %w", err)
    }
    if err := coverage.WriteCountersDir(fixtureCoverageDirectory); err != nil {
        return fmt.Errorf("fixture stage counters: %w", err)
    }
    return nil
}

func fixtureExecWithCoverage(execute func(string, []string, []string) error, path string, args, environment []string) error {
    if err := fixtureFlushCoverage(); err != nil {
        return err
    }
    return execute(path, args, environment)
}

// These counters describe actual owner work before its typed progress message,
// never final counters from workers the guardian later kills.
func fixtureSendWithCoverage(send func(int, []byte, []byte, unix.Sockaddr, int) (int, error), socket int, packet, control []byte, address unix.Sockaddr, flags int, kind byte) (int, error) {
    if kind == mountTransferReady || kind == mountTransferAck {
        if err := fixtureFlushCoverage(); err != nil {
            return 0, err
        }
    }
    return send(socket, packet, control, address, flags)
}
`

func prepareLaunchCoverage(directory, uidText, gidText string) error {
	uid, err := strconv.Atoi(uidText)
	if err != nil || uid <= 0 {
		return fmt.Errorf("invalid fixture coverage caller")
	}
	gid, err := strconv.Atoi(gidText)
	if err != nil || gid < 0 {
		return fmt.Errorf("invalid fixture coverage group")
	}
	collector := filepath.Join(directory, "coverage")
	if err := os.Mkdir(collector, 0o770); err != nil {
		return err
	}
	// Initial U_view startup has group 0 but no DAC authority over the host
	// filesystem; after its seal the same runtime has the caller's UID.
	if err := os.Chown(collector, uid, 0); err != nil {
		return err
	}
	return os.Chmod(collector, 0o770)
}

// Root-created counter files can inherit the launch's private umask. Expose
// only single-link runtime files in this fixture collector after settlement.
func makeLaunchCoverageReadable(directory, uidText string) error {
	uid, err := strconv.Atoi(uidText)
	if err != nil || uid <= 0 {
		return fmt.Errorf("invalid fixture coverage caller")
	}
	fd, err := unix.Open(filepath.Join(directory, "coverage"), unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var collector unix.Stat_t
	if err := unix.Fstat(fd, &collector); err != nil || collector.Uid != uint32(uid) || collector.Gid != 0 || collector.Mode&0o777 != 0o770 {
		return fmt.Errorf("native fixture collector custody changed")
	}
	entries, err := os.ReadDir(mountDescriptorPath(fd))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "covmeta.") && !strings.HasPrefix(entry.Name(), "covcounters.") {
			continue
		}
		file, err := unix.Openat(fd, entry.Name(), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		var stat unix.Stat_t
		err = unix.Fstat(file, &stat)
		if err == nil && (stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || (stat.Uid != 0 && stat.Uid != uint32(uid))) {
			err = fmt.Errorf("native runtime profile custody changed")
		}
		if err == nil {
			err = unix.Fchmod(file, 0o644)
		}
		_ = unix.Close(file)
		if err != nil {
			return err
		}
	}
	return nil
}

func observeLaunchCoverage(t *testing.T, directory string) error {
	t.Helper()
	owned, err := captureFixtureLifecyclePins()
	if err != nil {
		return err
	}
	defer owned.close()
	broker, err := captureFixtureOrdinaryBroker()
	if err != nil {
		return err
	}
	roles := []lifecycleRole{broker}
	for _, role := range owned.roles {
		if role.Role == "desktop" {
			guardian, err := lifecycleBirth(role.Parent)
			if err != nil {
				return err
			}
			guardian.Role = "guardian"
			roles = append(roles, guardian)
			continue
		}
		if role.Role == "mount-intake" {
			checkMountIntakePollAuthority(t, role.PID)
		}
		roles = append(roles, role)
	}
	raw, err := json.Marshal(roles)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "coverage-roles.json"), raw, 0o644)
}

func TestProtectedLaunchNativeOwnersRetainTheirCoverage(t *testing.T) {
	fixture := installLaunchFixture(t, measuredLaunchFixture)
	launch := startLaunch(t, fixture, []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}, "--broker-probe")
	facts := launch.facts
	ordinary := pinFixtureProcess(t, facts.DetachedPID, facts.DetachedStart, true)
	if facts.UIDs != [3]int{os.Getuid(), os.Getuid(), os.Getuid()} || facts.ReadError != "" || facts.ModuleUser != "EPERM" || facts.ModuleLibrary != "ok" || facts.InstallWrite != "EROFS" || facts.Remount != "EPERM" || facts.Namespace.OwnerUID != 0 {
		t.Fatalf("instrumented actual native boundary: %+v", facts)
	}
	if facts.BrokerError != "" || facts.BrokerExit != 0 || !facts.AbandonedEnded {
		t.Fatalf("instrumented actual broker lifecycle: %+v", facts)
	}
	if fixture.measured {
		testutil.FailErr(t, "observe authenticated covered roles", sudoFixture(t, "coverage-observe", fixture.dir,
			fixtureUIDEnv+"="+strconv.Itoa(os.Getuid()), fixtureDesktopPIDEnv+"="+strconv.Itoa(facts.PID),
			"PW_FIXTURE_DESKTOP_START="+strconv.FormatUint(facts.ProcessStarts[facts.PID], 10),
			"PW_FIXTURE_BOOTSTRAP_PID="+strconv.Itoa(launch.cmd.Process.Pid)))
	}
	testutil.FailErr(t, "actual mediated late-volume restrictions", sudoFixture(t, "module-view", fixture.dir,
		fixtureDesktopPIDEnv+"="+strconv.Itoa(facts.PID), fixtureUIDEnv+"="+strconv.Itoa(os.Getuid()),
		"PW_FIXTURE_DESKTOP_START="+strconv.FormatUint(facts.ProcessStarts[facts.PID], 10),
		"PW_FIXTURE_BOOTSTRAP_PID="+strconv.Itoa(launch.cmd.Process.Pid)))
	testutil.FailErr(t, "close covered desktop", launch.stdin.Close())
	testutil.FailErr(t, "settle actual native role runtimes", launch.wait())
	if fixtureProcessExited(ordinary) {
		t.Fatal("protected retirement ended the ordinary detached broker program")
	}
	testutil.FailErr(t, "release settled generation custody", sudoFixture(t, "lock-free", fixture.dir, fixtureHash+"="+fixture.digest))
	if fixture.measured {
		testutil.FailErr(t, "read owned runtime profiles", sudoFixture(t, "coverage-readable", fixture.dir, fixtureUIDEnv+"="+strconv.Itoa(os.Getuid())))
		raw, err := os.ReadFile(filepath.Join(fixture.dir, "coverage-roles.json"))
		testutil.FailErr(t, "read observed native roles", err)
		var roles []lifecycleRole
		testutil.FailErr(t, "decode observed native roles", json.Unmarshal(raw, &roles))
		collectLaunchCoverage(t, fixture.dir, roles)
	}
}

func collectLaunchCoverage(t *testing.T, directory string, roles []lifecycleRole) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(directory, "coverage"))
	testutil.FailErr(t, "read settled native role coverage", err)
	profiles := map[int]map[string]bool{}
	destination := os.Getenv("PW_GO_CHILD_COVERDIR")
	if destination == "" {
		destination = os.Getenv("GOCOVERDIR")
	}
	if destination == "" {
		t.Fatal("covered native fixture has no managed collector")
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "covmeta.") && !strings.HasPrefix(name, "covcounters.") {
			continue
		}
		parts := strings.Split(name, ".")
		if len(parts) == 4 && parts[0] == "covcounters" {
			pid, err := strconv.Atoi(parts[2])
			testutil.FailErr(t, "decode native counter process", err)
			if profiles[pid] == nil {
				profiles[pid] = map[string]bool{}
			}
			profiles[pid][parts[1]] = true
		}
		data, err := os.ReadFile(filepath.Join(directory, "coverage", name))
		testutil.FailErr(t, "read native runtime profile", err)
		testutil.FailErr(t, "retain native runtime profile", os.WriteFile(filepath.Join(destination, name), data, 0o600))
	}
	for _, role := range roles {
		minimum := 1
		if role.Role == "guardian" {
			minimum = 2 // Same PID: the old stage and the fresh entry runtime.
		}
		if len(profiles[role.PID]) < minimum {
			t.Fatalf("settled %s PID %d produced %d runtime profiles, want at least %d", role.Role, role.PID, len(profiles[role.PID]), minimum)
		}
	}
}
