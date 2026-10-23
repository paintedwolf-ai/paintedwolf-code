//go:build linux

package launchguard

import (
	"errors"
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

func TestMountSourcePrivilegedRoleDropsEveryThreadBeforeOpening(t *testing.T) {
	fixture := installLaunchFixture(t)
	testutil.FailErr(t, "exercise actual caller-credential startup role", sudoFixture(t, "mount-source", fixture.dir,
		fixtureUIDEnv+"="+strconv.Itoa(os.Getuid()), fixtureGIDEnv+"="+strconv.Itoa(os.Getgid())))
}

func checkMountSourceRole(directory, uidText, gidText string) error {
	runtime.LockOSThread()
	uid, uidErr := strconv.ParseUint(uidText, 10, 32)
	gid, gidErr := strconv.ParseUint(gidText, 10, 32)
	if uidErr != nil || gidErr != nil || uid == 0 {
		return errors.New("invalid source fixture caller")
	}
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return err
	}
	if err := unix.Mount("", "/", "", unix.MS_PRIVATE|unix.MS_REC, ""); err != nil {
		return err
	}
	policyPath, lockDirectory = filepath.Join(directory, "launch.json"), filepath.Join(directory, "locks")
	generation, err := CaptureGeneration()
	if err != nil {
		return err
	}
	defer generation.Close()
	frame, err := sealEntrySpec(EntrySpec{Version: 1, SupervisorPID: os.Getpid(), UID: uint32(uid), GID: uint32(gid), Groups: []uint32{uint32(gid)}, Generation: generation.Policy, Image: generation.DesktopImage})
	if err != nil {
		return err
	}
	defer func() { _ = frame.Close() }()
	origin, err := os.Open("/proc/self/ns/user")
	if err != nil {
		return err
	}
	defer func() { _ = origin.Close() }()
	info, err := os.Open("/proc/thread-self/mountinfo")
	if err != nil {
		return err
	}
	defer func() { _ = info.Close() }()
	raw, err := readMountInfo(int(info.Fd()), nil)
	if err != nil {
		return err
	}
	entries, err := parseMountTopology(raw)
	if err != nil {
		return err
	}
	opener, err := startMountSource(frame, origin, generation.Entry, raw)
	if err != nil {
		return err
	}
	defer opener.close()
	root, found := mountEntry{}, false
	for _, entry := range entries {
		if entry.mountPoint == "/" {
			root, found = entry, true
		}
	}
	if !found {
		return errors.New("fixture namespace root missing")
	}
	fd, err := opener.open(root)
	if err != nil {
		return err
	}
	_ = unix.Close(fd)
	if err := verifySourceThreadCredentials(opener.process.Process.Pid, uidText, gidText); err != nil {
		return err
	}
	// A same-UID direct parent is allowed to trace an ordinary exec under the
	// host's ptrace policy. Drop this fixture's root authority, demonstrate that
	// positive control, then require refusal for the actual isolated opener.
	if err := dropPrivileges(Identity{UID: uint32(uid), GID: uint32(gid), Groups: []uint32{uint32(gid)}}); err != nil {
		return err
	}
	if err := checkMountSourcePtrace(opener.process.Process.Pid); err != nil {
		return err
	}
	return opener.finish()
}

func checkMountSourcePtrace(sourcePID int) error {
	control := exec.Command("/bin/sleep", "30")
	if err := control.Start(); err != nil {
		return err
	}
	defer func() { _ = control.Process.Kill(); _ = control.Wait() }()
	if err := unix.PtraceAttach(control.Process.Pid); err != nil {
		return fmt.Errorf("ordinary same-user ptrace positive control: %w", err)
	}
	var status unix.WaitStatus
	if _, err := unix.Wait4(control.Process.Pid, &status, unix.WUNTRACED, nil); err != nil || !status.Stopped() {
		return errors.New("ordinary ptrace target did not stop")
	}
	if err := unix.PtraceDetach(control.Process.Pid); err != nil {
		return err
	}
	if err := unix.PtraceAttach(sourcePID); !errors.Is(err, unix.EPERM) {
		if err == nil {
			_, _ = unix.Wait4(sourcePID, &status, unix.WUNTRACED, nil)
			_ = unix.PtraceDetach(sourcePID)
		}
		return fmt.Errorf("source opener trace result %v, want EPERM", err)
	}
	return nil
}

func verifySourceThreadCredentials(pid int, uid, gid string) error {
	directory := fmt.Sprintf("/proc/%d/task", pid)
	threads, err := os.ReadDir(directory)
	if err != nil || len(threads) == 0 {
		return errors.New("source opener has no observable threads")
	}
	for _, thread := range threads {
		raw, err := os.ReadFile(filepath.Join(directory, thread.Name(), "status"))
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for line := range strings.SplitSeq(string(raw), "\n") {
			key, value, found := strings.Cut(line, ":")
			if !found {
				continue
			}
			values := strings.Fields(value)
			switch key {
			case "Uid", "Gid":
				want := uid
				if key == "Gid" {
					want = gid
				}
				if len(values) != 4 || strings.Join(values, " ") != strings.TrimSpace(strings.Repeat(want+" ", 4)) {
					return fmt.Errorf("source thread %s %s differs from caller", thread.Name(), key)
				}
			case "Groups":
				if len(values) != 1 || values[0] != gid {
					return errors.New("source supplementary groups differ from caller")
				}
			case "CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb":
				if len(values) != 1 || strings.Trim(values[0], "0") != "" {
					return fmt.Errorf("source thread %s retains %s", thread.Name(), key)
				}
			case "NoNewPrivs":
				if len(values) != 1 || values[0] != "1" {
					return errors.New("source privilege transition remains enabled")
				}
			default:
				continue
			}
			seen[key] = true
		}
		for _, key := range []string{"Uid", "Gid", "Groups", "CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb", "NoNewPrivs"} {
			if !seen[key] {
				return fmt.Errorf("source thread lacks %s", key)
			}
		}
	}
	return nil
}
