//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestMountPipelineUsesPrivateWriterAndSameUserNamespaceIntake(t *testing.T) {
	fixture := installLaunchFixture(t)
	testutil.FailErr(t, "start actual fixed-role mount pipeline", sudoFixture(t, "mount-pipeline", fixture.dir,
		fixtureUIDEnv+"="+strconv.Itoa(os.Getuid()), fixtureGIDEnv+"="+strconv.Itoa(os.Getgid())))
}

func checkPrivateMountPipeline(directory, uidText, gidText string) error {
	runtime.LockOSThread()
	uid, uidErr := strconv.ParseUint(uidText, 10, 32)
	gid, gidErr := strconv.ParseUint(gidText, 10, 32)
	if uidErr != nil || gidErr != nil || uid == 0 {
		return errors.New("invalid mount pipeline caller")
	}
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return err
	}
	policyPath, lockDirectory = filepath.Join(directory, "launch.json"), filepath.Join(directory, "locks")
	generation, err := CaptureGeneration()
	if err != nil {
		return err
	}
	defer generation.Close()
	mounted, err := bindGeneration(generation.Policy, generation.Directory, generation.Desktop)
	if err != nil {
		return err
	}
	defer func() { _ = mounted.Close() }()
	if err := checkSelectedModuleCodeRoots(generation.Policy, mounted); err != nil {
		return err
	}
	spec := EntrySpec{Version: 1, SupervisorPID: os.Getpid(), UID: uint32(uid), GID: uint32(gid), Groups: []uint32{uint32(gid)}, Generation: generation.Policy, Image: generation.DesktopImage}
	frame, err := sealEntrySpec(spec)
	if err != nil {
		return err
	}
	defer func() { _ = frame.Close() }()
	origin, err := os.Open("/proc/self/ns/user")
	if err != nil {
		return err
	}
	defer func() { _ = origin.Close() }()
	prepared, err := prepareModuleView(spec, frame, origin, generation.Entry, mounted)
	if err != nil {
		return err
	}
	defer prepared.close()
	view, err := startPrivateModuleView(frame, generation.Entry, origin, generation.MountTransition, prepared)
	if err != nil {
		return err
	}
	settled := false
	defer func() {
		if !settled {
			view.abort()
		}
	}()
	wantUID := strings.TrimSpace(strings.Repeat(uidText+" ", 4))
	wantGID := strings.TrimSpace(strings.Repeat(gidText+" ", 4))
	for _, pid := range []int{view.watcher.Process.Pid, view.intake.Process.Pid} {
		if err := verifyWatcherThreads(pid, wantUID, wantGID, gidText); err != nil {
			return err
		}
	}
	watchUser, err := os.Stat(fmt.Sprintf("/proc/%d/ns/user", view.watcher.Process.Pid))
	if err != nil {
		return err
	}
	intakeUser, err := os.Stat(fmt.Sprintf("/proc/%d/ns/user", view.intake.Process.Pid))
	if err != nil || !os.SameFile(watchUser, intakeUser) {
		return errors.New("intake does not share the writer's U_view")
	}
	watchMount, err := os.Stat(fmt.Sprintf("/proc/%d/ns/mnt", view.watcher.Process.Pid))
	if err != nil {
		return err
	}
	intakeMount, err := os.Stat(fmt.Sprintf("/proc/%d/ns/mnt", view.intake.Process.Pid))
	if err != nil || os.SameFile(watchMount, intakeMount) {
		return errors.New("intake shares the private app mount namespace")
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/mountinfo", view.watcher.Process.Pid))
	if err != nil {
		return err
	}
	entries, err := parseMountTopology(raw)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.shared != 0 || entry.master != 0 || entry.propagateFrom != 0 {
			return errors.New("app view retains propagation")
		}
	}
	if err := expectMountViewReady(int(view.health.Fd()), uint64(len(prepared.plan.initial))+3); err != nil {
		return err
	}
	var retired []unix.PollFd
	defer func() {
		for _, pin := range retired {
			_ = unix.Close(int(pin.Fd))
		}
	}()
	for _, role := range view.pidfds {
		pin, err := unix.Dup(int(role.Fd()))
		if err != nil {
			return err
		}
		pollFD, err := guardianPollDescriptor(uintptr(pin))
		if err != nil {
			_ = unix.Close(pin)
			return err
		}
		retired = append(retired, unix.PollFd{Fd: pollFD, Events: unix.POLLIN})
	}
	view.abort()
	settled = true
	if _, err := unix.Poll(retired, 0); err != nil {
		return err
	}
	for _, role := range retired {
		if role.Revents&unix.POLLIN == 0 || role.Revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
			return errors.New("private mount roles did not settle after their lifetime ended")
		}
	}
	return nil
}

func checkSelectedModuleCodeRoots(policy GenerationPolicy, desktop *os.File) error {
	interpreter, err := programInterpreter(desktop)
	if err != nil {
		return err
	}
	roots, err := captureModuleCodeRoots(policy, desktop)
	if err != nil {
		return err
	}
	defer closeModuleCodeRoots(roots)
	expected := moduleRoots(policy, interpreter)
	sort.Strings(expected)
	expected = append(slices.Compact(expected), policy.LogicalInstallRoot)
	var actual []string
	for _, root := range roots {
		if root.object == nil || root.tree == nil {
			return errors.New("selected module root lacks retained object and detached tree")
		}
		actual = append(actual, root.path)
	}
	if !slices.Equal(actual, expected) {
		return fmt.Errorf("selected code roots %v differ from declared closure %v", actual, expected)
	}
	return verifyModuleCodeRoots(roots)
}
