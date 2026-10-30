//go:build linux

package launchguard

import (
	"errors"
	"os"
	"runtime"

	"github.com/lycaon/lycaon/internal/launchbroker"
	"golang.org/x/sys/unix"
)

// MountIntakeArgument is reached only after the fixed native transition has
// selected the original slave mount namespace and the authenticated U_view.
const MountIntakeArgument = "--mount-intake"

const (
	intakeSpecFD = 3 + iota
	intakeLifetimeFD
	intakeSocketFD
	intakeSelfFD
)

func RunMountIntake() error {
	runtime.LockOSThread()
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	if uid, effective, saved := unix.Getresuid(); uid != 0 || effective != 0 || saved != 0 {
		return errors.New("module intake requires the authenticated transition")
	}
	if gid, effective, saved := unix.Getresgid(); gid != 0 || effective != 0 || saved != 0 {
		return errors.New("module intake requires the authenticated groups")
	}
	if err := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return err
	}
	frame := os.NewFile(intakeSpecFD, "sealed-entry-spec")
	spec, err := readEntry(frame)
	_ = frame.Close()
	if err != nil {
		return err
	}
	peer, err := launchbroker.CheckSocket(intakeSocketFD)
	if err != nil || peer.Uid != 0 || int(peer.Pid) != os.Getppid() {
		return errors.New("module intake socket is not the retained stage pair")
	}
	if err := watcherUserNamespace(); err != nil {
		return err
	}
	for _, name := range []string{"uid_map", "gid_map"} {
		raw, err := os.ReadFile("/proc/self/" + name)
		if err != nil || !fullIdentityMap(string(raw)) {
			return errors.New("module intake identity mapping differs from the fixed transition")
		}
	}
	var lifetime unix.Stat_t
	if err := unix.Fstat(intakeLifetimeFD, &lifetime); err != nil || lifetime.Mode&unix.S_IFMT != unix.S_IFIFO {
		return errors.New("invalid module intake lifetime")
	}
	message, planFD, err := receiveMountTransfer(intakeSocketFD)
	if err != nil {
		return err
	}
	if message != (mountTransfer{kind: mountTransferPlan, sequence: 1}) || planFD < 0 {
		if planFD >= 0 {
			_ = unix.Close(planFD)
		}
		return errors.New("module intake lacks its root-sealed startup plan")
	}
	planFile := os.NewFile(uintptr(planFD), "sealed-module-view-plan")
	plan, err := readModuleViewPlan(planFile)
	_ = planFile.Close()
	if err != nil {
		return err
	}
	_ = unix.Close(intakeSelfFD)
	if err := sealModuleWatcher(Identity{UID: spec.UID, GID: spec.GID, Groups: spec.Groups}); err != nil {
		return err
	}
	defer func() { _ = unix.Close(intakeSocketFD); _ = unix.Close(intakeLifetimeFD) }()
	return monitorModuleIntake(spec, plan, intakeSocketFD, intakeLifetimeFD)
}
