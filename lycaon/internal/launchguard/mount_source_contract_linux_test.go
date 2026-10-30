//go:build linux

package launchguard

import (
	"context"
	"encoding/binary"
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

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestMountSourcePrivilegedRoleDropsEveryThreadBeforeOpening(t *testing.T) {
	fixture := installLaunchFixture(t)
	testutil.FailErr(t, "exercise actual caller-credential startup role", sudoFixture(t, "mount-source", fixture.dir,
		fixtureUIDEnv+"="+strconv.Itoa(os.Getuid()), fixtureGIDEnv+"="+strconv.Itoa(os.Getgid())))
}

const fixtureSourcePathEnv = "PW_MOUNT_SOURCE_PATH"

func checkMountSourceRole(directory, uidText, gidText, sourcePath string) error {
	if sourcePath == "" {
		sourcePath = "/"
	}
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
	if err := checkSourceFrameRefusals(frame, origin, generation.Entry, raw, uint32(uid)); err != nil {
		return err
	}
	opener, err := startMountSource(frame, origin, generation.Entry, raw)
	if err != nil {
		return err
	}
	defer opener.close()
	root, found := mountEntry{}, false
	for _, entry := range entries {
		if entry.mountPoint == sourcePath {
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
		return errors.Join(errors.New("source opener trace did not refuse with EPERM"), err)
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

func checkSourceFrameRefusals(frame, origin, entry *os.File, topology []byte, caller uint32) error {
	sealed, err := sealSourceTopology(topology)
	if err != nil {
		return err
	}
	defer func() { _ = sealed.Close() }()
	raw, err := os.ReadFile(mountDescriptorPath(int(sealed.Fd())))
	if err != nil {
		return err
	}
	for _, mode := range []string{"namespace", "version", "reserved", "truncated", "foreign owner"} {
		bad := append([]byte(nil), raw...)
		switch mode {
		case "namespace":
			binary.LittleEndian.PutUint64(bad[24:], binary.LittleEndian.Uint64(bad[24:])^1)
		case "version":
			binary.LittleEndian.PutUint32(bad[8:], 2)
		case "reserved":
			bad[12] = 1
		case "truncated":
			bad = bad[:sourceTopologyHeader-1]
		}
		fd, err := unix.MemfdCreate("invalid-source-frame", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
		if err != nil {
			return err
		}
		invalid := os.NewFile(uintptr(fd), "invalid-source-frame")
		_, err = invalid.Write(bad)
		if err == nil && mode == "foreign owner" {
			err = invalid.Chown(int(caller), -1)
		}
		if err == nil {
			err = invalid.Chmod(0o400)
		}
		if err == nil {
			_, err = unix.FcntlInt(invalid.Fd(), unix.F_ADD_SEALS, entrySeals)
		}
		if err == nil {
			err = checkSourceRoleRefuses(frame, origin, entry, invalid)
		}
		_ = invalid.Close()
		if err != nil {
			return fmt.Errorf("source %s refusal: %w", mode, err)
		}
	}
	return nil
}

func checkSourceRoleRefuses(frame, origin, entry, topology *os.File) error {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	parent, child := os.NewFile(uintptr(pair[0]), "refusal-parent"), os.NewFile(uintptr(pair[1]), "refusal-child")
	defer func() { _ = parent.Close(); _ = child.Close() }()
	read, write, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = read.Close(); _ = write.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/proc/self/fd/"+strconv.Itoa(sourceSelfFD), MountSourceArgument)
	command.Env = []string{}
	command.ExtraFiles = []*os.File{frame, origin, topology, child, read, entry}
	command.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 unix.CLONE_NEWUSER,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: int(^uint32(0))}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: int(^uint32(0))}},
		GidMappingsEnableSetgroups: true,
	}
	out, runErr := command.CombinedOutput()
	if ctx.Err() != nil || runErr == nil || command.ProcessState == nil || command.ProcessState.ExitCode() != 1 || len(out) != 0 {
		return errors.Join(fmt.Errorf("source refusal state=%v output=%q", command.ProcessState, out), ctx.Err(), runErr)
	}
	return nil
}
