//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	defer view.abort()
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
	return expectMountViewReady(int(view.health.Fd()), uint64(len(prepared.plan.initial))+3)
}
