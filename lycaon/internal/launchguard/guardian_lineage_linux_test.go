//go:build linux

package launchguard

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

const fixtureFailedRoleEnv = "PW_FAILED_MOUNT_ROLE"

func TestGuardianFatalMountWorkerExitSettlesOnlyItsProtectedLineage(t *testing.T) {
	fixture := installLaunchFixture(t)
	for _, role := range []string{MountViewArgument, MountIntakeArgument} {
		t.Run(role, func(t *testing.T) {
			unrelated := startLaunch(t, fixture, []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"})
			victim := startLaunch(t, fixture, []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}, "--broker-probe", "--lineage-probe")
			if victim.facts.ReadError != "" || len(victim.facts.LineagePIDs) != 4 || victim.facts.DetachedPID <= 1 {
				t.Fatalf("protected lineage fixture was not established: %+v", victim.facts)
			}
			var protected []int
			for _, pid := range append([]int{victim.facts.PID}, victim.facts.LineagePIDs...) {
				protected = append(protected, pinFixtureProcess(t, pid, victim.facts.ProcessStarts[pid], true))
			}
			ordinary := pinFixtureProcess(t, victim.facts.DetachedPID, victim.facts.DetachedStart, true)
			other := pinFixtureProcess(t, unrelated.facts.PID, unrelated.facts.ProcessStarts[unrelated.facts.PID], false)
			testutil.FailErr(t, "stop only the selected launch mount worker", sudoFixture(t, "fail-mount-role", fixture.dir, fixtureDesktopPIDEnv+"="+strconv.Itoa(victim.facts.PID), fixtureFailedRoleEnv+"="+role))
			select {
			case <-victim.result.done:
			case <-time.After(10 * time.Second):
				t.Fatal("fatal mount worker exit did not settle the protected lineage")
			}
			if victim.wait() == nil {
				t.Fatal("fatal mount worker exit reported successful desktop completion")
			}
			for _, fd := range protected {
				if !fixtureProcessExited(fd) {
					t.Fatal("owned protected descendant survived settled launch")
				}
			}
			if fixtureProcessExited(ordinary) {
				t.Fatal("fatal cleanup killed the ordinary detached broker program")
			}
			if fixtureProcessExited(other) {
				t.Fatal("fatal cleanup killed another protected launch")
			}
			_ = unrelated.stdin.Close()
			testutil.FailErr(t, "settle the unaffected protected launch normally", unrelated.wait())
			testutil.FailErr(t, "release generation custody only after protected descendants settle", sudoFixture(t, "lock-free", fixture.dir, fixtureHash+"="+fixture.digest))
		})
	}
}

func pinFixtureProcess(t *testing.T, pid int, start uint64, cleanup bool) int {
	t.Helper()
	if start == 0 {
		t.Fatal("owned fixture process lacks creator-recorded birth")
	}
	directory, err := unix.Open(fmt.Sprintf("/proc/%d", pid), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	testutil.FailErr(t, "pin original fixture proc directory", err)
	defer unix.Close(directory)
	fd, err := unix.PidfdOpen(pid, 0)
	testutil.FailErr(t, "pin the exact owned fixture process", err)
	if err := verifyFixtureBirth(directory, pid, start, uint32(os.Getuid())); err != nil {
		_ = unix.Close(fd)
		testutil.FailErr(t, "reject a replaced fixture process before signal authority", err)
	}
	t.Cleanup(func() {
		if cleanup {
			_ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
		}
		_ = unix.Close(fd)
	})
	return fd
}

func verifyFixtureBirth(directory, pid int, start uint64, uid uint32) error {
	read := func(name string) ([]byte, error) {
		fd, err := unix.Openat(directory, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		file := os.NewFile(uintptr(fd), "pinned-fixture-birth")
		defer file.Close()
		return os.ReadFile(mountDescriptorPath(fd))
	}
	raw, err := read("stat")
	if err != nil {
		return err
	}
	text := string(raw)
	begin, end := strings.IndexByte(text, '('), strings.LastIndexByte(text, ')')
	if begin < 1 || end < begin {
		return errors.New("fixture birth lacks process command")
	}
	actual, e1 := strconv.Atoi(strings.TrimSpace(text[:begin]))
	fields := strings.Fields(text[end+1:])
	if e1 != nil || actual != pid || len(fields) < 20 {
		return errors.New("fixture birth names another process")
	}
	parent, e2 := strconv.Atoi(fields[1])
	born, e3 := strconv.ParseUint(fields[19], 10, 64)
	if e2 != nil || e3 != nil || parent <= 0 || born != start {
		return errors.New("fixture process birth differs from creator observation")
	}
	status, err := read("status")
	if err != nil {
		return err
	}
	found := false
	for _, line := range strings.Split(string(status), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || key != "Uid" {
			continue
		}
		values := strings.Fields(value)
		if found || len(values) != 4 {
			return errors.New("ambiguous fixture process UID")
		}
		found = true
		for _, field := range values {
			actual, err := strconv.ParseUint(field, 10, 32)
			if err != nil || actual != uint64(uid) {
				return errors.New("fixture process has another kernel identity")
			}
		}
	}
	if !found {
		return errors.New("fixture process UID is absent")
	}
	return nil
}

func fixtureProcessExited(fd int) bool {
	polls := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	_, err := unix.Poll(polls, 0)
	return err == nil && polls[0].Revents&unix.POLLIN != 0
}

// The fixture signals only an exact selected-image child of the desktop's
// guardian. Pin the proc directory before pidfd_open and revalidate afterwards;
// PID reuse never authorizes a signal to a replacement process.
func failFixtureMountRole(directory, pidText, role string) error {
	if role != MountViewArgument && role != MountIntakeArgument {
		return errors.New("unknown fixed mount fixture role")
	}
	desktopPID, err := strconv.Atoi(pidText)
	if err != nil || desktopPID <= 1 {
		return errors.New("invalid owned desktop fixture PID")
	}
	policyPath, lockDirectory = filepath.Join(directory, "launch.json"), filepath.Join(directory, "locks")
	generation, err := CaptureGeneration()
	if err != nil {
		return err
	}
	defer generation.Close()
	desktop, err := os.Open(fmt.Sprintf("/proc/%d/exe", desktopPID))
	if err != nil {
		return err
	}
	defer desktop.Close()
	actual, err := desktop.Stat()
	if err != nil {
		return err
	}
	selected, err := generation.Desktop.Stat()
	if err != nil || !os.SameFile(actual, selected) {
		return errors.New("fixture desktop differs from its selected generation")
	}
	status, err := readProcStatusFields(fmt.Sprintf("/proc/%d/status", desktopPID))
	if err != nil {
		return err
	}
	guardian, err := strconv.Atoi(status["PPid"])
	if err != nil || guardian <= 1 {
		return errors.New("owned desktop guardian missing")
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", guardian, guardian))
	if err != nil {
		return err
	}
	children := bytes.Fields(raw)
	for _, child := range children {
		pid, err := strconv.Atoi(string(child))
		if err != nil || pid <= 1 {
			return errors.New("invalid guardian child census")
		}
		directory, err := unix.Open(fmt.Sprintf("/proc/%d", pid), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		pin, err := unix.PidfdOpen(pid, 0)
		if err != nil {
			_ = unix.Close(directory)
			return err
		}
		matched, verifyErr := matchFixtureMountRole(directory, guardian, role, generation.Entry)
		if matched && verifyErr == nil {
			err = unix.PidfdSendSignal(pin, unix.SIGKILL, nil, 0)
			_ = unix.Close(pin)
			_ = unix.Close(directory)
			return err
		}
		_ = unix.Close(pin)
		_ = unix.Close(directory)
		if verifyErr != nil {
			return verifyErr
		}
	}
	return errors.New("selected owned mount worker was not found")
}

func matchFixtureMountRole(directory, guardian int, role string, selected *os.File) (bool, error) {
	read := func(name string) ([]byte, error) {
		fd, err := unix.Openat(directory, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		f := os.NewFile(uintptr(fd), "fixture-process-fact")
		defer f.Close()
		return os.ReadFile(mountDescriptorPath(fd))
	}
	command, err := read("cmdline")
	if err != nil {
		return false, err
	}
	if !bytes.Equal(command, []byte("pw-launch-entry\x00"+role+"\x00")) {
		return false, nil
	}
	status, err := read("status")
	if err != nil {
		return false, err
	}
	if !bytes.Contains(status, []byte("PPid:\t"+strconv.Itoa(guardian)+"\n")) {
		return false, errors.New("selected worker is no longer owned by the guardian")
	}
	fd, err := unix.Openat(directory, "exe", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	image := os.NewFile(uintptr(fd), "owned-worker-image")
	defer image.Close()
	actual, err := image.Stat()
	if err != nil {
		return false, err
	}
	wanted, err := selected.Stat()
	if err != nil || !os.SameFile(actual, wanted) {
		return false, errors.New("owned worker differs from the selected fixed entry")
	}
	return true, nil
}

func TestGuardianFixturePIDPinRejectsChangedBirthOrKernelIdentity(t *testing.T) {
	directory, err := unix.Open("/proc/self", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	testutil.FailErr(t, "pin own process for birth refusal control", err)
	defer unix.Close(directory)
	raw, err := os.ReadFile("/proc/self/stat")
	testutil.FailErr(t, "read own kernel birth", err)
	fields := strings.Fields(string(raw[bytes.LastIndexByte(raw, ')')+1:]))
	start, err := strconv.ParseUint(fields[19], 10, 64)
	testutil.FailErr(t, "decode own kernel birth", err)
	testutil.FailErr(t, "accept pinned same-process birth", verifyFixtureBirth(directory, os.Getpid(), start, uint32(os.Getuid())))
	if verifyFixtureBirth(directory, os.Getpid(), start+1, uint32(os.Getuid())) == nil {
		t.Fatal("replaced process birth granted fixture cleanup authority")
	}
	if verifyFixtureBirth(directory, os.Getpid(), start, uint32(os.Getuid())^1) == nil {
		t.Fatal("foreign kernel identity granted fixture cleanup authority")
	}
	if verifyFixtureBirth(directory, os.Getpid()+1, start, uint32(os.Getuid())) == nil {
		t.Fatal("another PID granted fixture cleanup authority")
	}
}
