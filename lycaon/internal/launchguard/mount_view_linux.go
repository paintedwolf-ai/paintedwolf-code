//go:build linux

package launchguard

import (
	"errors"
	"os"
	"runtime"
	"time"

	"github.com/lycaon/lycaon/internal/launchbroker"
	"golang.org/x/sys/unix"
)

// MountViewArgument selects the fixed private-view writer role.
const MountViewArgument = "--mount-view"

const (
	viewSpecFD = 3 + iota
	viewLifetimeFD
	viewStageFD
	viewSelfFD
	viewPlanFD
	viewIntakeFD
	viewOriginFD
)

// RunMountView is the sole app-namespace mount writer. The stage creates its
// root-owned full-map U_view and minimal private M_view, then transfers only
// detached, already tightened initial trees. Caller credentials are sealed on
// every thread before the first target walk, including caller-only FUSE paths.
func RunMountView() error {
	runtime.LockOSThread()
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	if uid, effective, saved := unix.Getresuid(); uid != 0 || effective != 0 || saved != 0 {
		return errors.New("module-view writer requires the root stage transition")
	}
	if gid, effective, saved := unix.Getresgid(); gid != 0 || effective != 0 || saved != 0 {
		return errors.New("module-view writer requires the root stage groups")
	}
	if err := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return err
	}
	frame := os.NewFile(viewSpecFD, "sealed-entry-spec")
	spec, err := readEntry(frame)
	_ = frame.Close()
	if err != nil {
		return err
	}
	planFile := os.NewFile(viewPlanFD, "sealed-module-view-plan")
	plan, err := readModuleViewPlan(planFile)
	_ = planFile.Close()
	if err != nil {
		return err
	}
	origin := os.NewFile(viewOriginFD, "initial-user-namespace")
	defer func() { _ = origin.Close() }()
	if err := verifyNamespace(origin); err != nil {
		return err
	}
	if err := watcherUserNamespace(); err != nil {
		return err
	}
	for _, socket := range []int{viewStageFD, viewIntakeFD} {
		peer, err := launchbroker.CheckSocket(socket)
		if err != nil || peer.Uid != 0 || int(peer.Pid) != os.Getppid() {
			return errors.New("module-view socket is not the retained stage pair")
		}
	}
	var lifetime unix.Stat_t
	if err := unix.Fstat(viewLifetimeFD, &lifetime); err != nil || lifetime.Mode&unix.S_IFMT != unix.S_IFIFO {
		return errors.New("invalid module-view lifetime")
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return err
	}
	_ = unix.Close(viewSelfFD)
	if err := sealModuleWatcher(Identity{UID: spec.UID, GID: spec.GID, Groups: spec.Groups}); err != nil {
		return err
	}
	return runPrivateMountView(plan, viewStageFD, viewIntakeFD, viewLifetimeFD)
}

func runPrivateMountView(plan moduleViewPlan, stage, intake, lifetime int) (err error) {
	defer func() { _ = unix.Close(stage); _ = unix.Close(intake); _ = unix.Close(lifetime) }()
	watchdog := time.AfterFunc(mountSourceSetupLimit, func() { os.Exit(1) })
	defer watchdog.Stop()
	writer, err := receiveInitialMountLeases(stage, plan)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, writer.close()) }()
	intakePoll, err := guardianPollDescriptor(uintptr(intake))
	if err != nil {
		return err
	}
	lifetimePoll, err := guardianPollDescriptor(uintptr(lifetime))
	if err != nil {
		return err
	}
	// The first stage readiness only permits establishing the fixed intake.
	// Desktop readiness follows complete source reconciliation below.
	stageSequence := uint64(len(plan.initial)) + 1
	if err := sendMountTransfer(stage, mountTransfer{kind: mountTransferReady, sequence: stageSequence}, -1); err != nil {
		return err
	}
	lastIntake := time.Now()
	for {
		watchdog.Reset(mountSourceSetupLimit)
		fds := []unix.PollFd{{Fd: intakePoll, Events: unix.POLLIN}, {Fd: lifetimePoll, Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR}}
		if _, err := unix.Poll(fds, 1000); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return err
		}
		if fds[1].Revents != 0 {
			return nil
		}
		if fds[0].Revents == 0 {
			if time.Since(lastIntake) >= mountSourceSetupLimit {
				return errors.New("module intake heartbeat deadline exceeded")
			}
			continue
		}
		lastIntake = time.Now()
		message, tree, err := receiveMountTransfer(intake)
		if err != nil {
			return err
		}
		if message.kind == mountTransferReady {
			if tree >= 0 {
				_ = unix.Close(tree)
				return errors.New("intake readiness carried a descriptor")
			}
			if writer.sequence == ^uint64(0) || message.sequence != writer.sequence+1 {
				return errors.New("intake readiness sequence changed or exhausted")
			}
			writer.sequence = message.sequence
		} else if err := writer.accept(message, tree); err != nil {
			var unavailable *mountDestinationError
			if message.kind != mountTransferAdd || !errors.As(err, &unavailable) {
				return err
			}
			message.kind = mountTransferDenied
			if err := sendMountTransfer(intake, message, -1); err != nil {
				return err
			}
			continue
		}
		if err := sendMountTransfer(intake, mountTransfer{kind: mountTransferAck, sequence: message.sequence}, -1); err != nil {
			return err
		}
		if message.kind == mountTransferReady {
			if stageSequence == ^uint64(0) {
				return errors.New("module view health sequence exhausted")
			}
			stageSequence++
			if err := sendMountTransfer(stage, mountTransfer{kind: mountTransferReady, sequence: stageSequence}, -1); err != nil {
				return err
			}
		}
	}
}
