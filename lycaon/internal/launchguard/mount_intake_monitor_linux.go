//go:build linux

package launchguard

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"golang.org/x/sys/unix"
)

// A stalled caller filesystem can only terminate this fixed, unprivileged
// intake. Its writer observes EOF and the launch lifecycle handles failure.
// No source mount descriptor remains pinned while waiting for another event.
func monitorModuleIntake(spec EntrySpec, plan moduleViewPlan, socket, lifetime int) error {
	watchdog := time.AfterFunc(mountSourceSetupLimit, func() { os.Exit(int(intakeFailureWatchdog)) })
	defer watchdog.Stop()
	// This reader owns its event acknowledgements. os.Open registers mountinfo
	// with Go netpoll, whose epoll callback can consume the same epoch event.
	info, err := unix.Open("/proc/thread-self/mountinfo", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(info) }()
	_, current, err := readCurrentMountTopology()
	if err != nil {
		return err
	}
	source, err := parseMountTopology(plan.source)
	if err != nil {
		return err
	}
	structural, err := correlateStructuralMounts(source, plan.structural, current)
	if err != nil {
		return err
	}
	// Repeat actual closed-role custody in M_intake after the full caller
	// identity seal; matching mountinfo text alone never grants an exemption.
	desktop, err := openRootControlled(filepath.Join(spec.Generation.LogicalInstallRoot, "bin/painted-wolf-code"), false)
	if err != nil {
		return err
	}
	if err = pkgmanifest.VerifyOpened(desktop, spec.Image); err != nil {
		_ = desktop.Close()
		return err
	}
	actual, err := structuralMounts(spec.Generation, desktop, current)
	_ = desktop.Close()
	if err != nil || !sameStructuralSelection(actual, structural) {
		return errors.New("intake structural custody differs from sealed selection")
	}
	intake := &moduleIntake{socket: socket, sequence: uint64(len(plan.initial))}
	for {
		_, current, err = readCurrentMountTopology()
		if err != nil {
			return err
		}
		if err = intake.adoptInitial(plan, current); errors.Is(err, errIntakeEpochChanged) {
			continue
		}
		if err != nil {
			return err
		}
		break
	}
	structuralFacts := make(map[int]mountEntry, len(structural))
	for _, entry := range current {
		if structural[entry.id] {
			structuralFacts[entry.id] = entry
		}
	}
	pending, err := intake.reconcile(current, structural, structuralFacts)
	if err != nil && !errors.Is(err, errIntakeEpochChanged) {
		return err
	}
	for errors.Is(err, errIntakeEpochChanged) {
		_, current, err = readCurrentMountTopology()
		if err != nil {
			return err
		}
		pending, err = intake.reconcile(current, structural, structuralFacts)
	}
	if err != nil {
		return err
	}
	if err := intake.exchange(mountTransferReady, 0, "", -1); err != nil {
		return err
	}
	lastHeartbeat := time.Now()
	for {
		watchdog.Reset(mountSourceSetupLimit)
		fds := []unix.PollFd{{Fd: int32(info), Events: unix.POLLPRI | unix.POLLERR}, {Fd: int32(lifetime), Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR}, {Fd: int32(socket), Events: unix.POLLIN}}
		pollWait := 100
		if pending {
			pollWait = 0 // An observed topology change already requires reconciliation.
		}
		if _, err := unix.Poll(fds, pollWait); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return &intakeMonitorFailure{intakeFailurePoll, err}
		}
		if fds[1].Revents != 0 {
			return nil
		}
		if fds[2].Revents != 0 {
			return &intakeMonitorFailure{intakeFailureControl, errors.New("unsolicited intake control or writer closure")}
		}
		if fds[0].Revents != 0 || pending {
			var changed bool
			current, changed, err = readIntakeMountTopology(info, readMountInfo)
			if err != nil {
				return err
			}
			if changed {
				pending = true
				continue
			}
			pending, err = intake.reconcile(current, structural, structuralFacts)
			if errors.Is(err, errIntakeEpochChanged) {
				pending = true
				continue
			}
			if err != nil {
				return &intakeMonitorFailure{intakeFailureReconcile, err}
			}
		}
		if time.Since(lastHeartbeat) >= time.Second {
			if err := intake.exchange(mountTransferReady, 0, "", -1); err != nil {
				return &intakeMonitorFailure{intakeFailureHeartbeat, err}
			}
			lastHeartbeat = time.Now()
		}
	}
}

func (intake *moduleIntake) reconcile(current []mountEntry, structural map[int]bool, facts map[int]mountEntry) (bool, error) {
	remaining := make(map[int]mountEntry, len(facts))
	for id, entry := range facts {
		remaining[id] = entry
	}
	for _, entry := range current {
		if expected, exists := remaining[entry.id]; exists {
			if expected != entry {
				return false, &intakeReconcileFailure{intakeReconcileStructural, errors.New("intake structural source changed")}
			}
			delete(remaining, entry.id)
		}
	}
	if len(remaining) > 0 {
		return false, &intakeReconcileFailure{intakeReconcileStructural, errors.New("intake structural source disappeared")}
	}
	if err := intake.retireChanged(current); err != nil {
		if errors.Is(err, errIntakeEpochChanged) {
			return false, err
		}
		return false, &intakeReconcileFailure{intakeReconcileRetirement, err}
	}
	return intake.importArrivals(current, structural)
}

// These closed groups describe only existing fatal reconciliation returns.
// They preserve the underlying cause and introduce no retry decision.
type intakeReconcileLeaf uint8

const (
	intakeReconcileStructural intakeReconcileLeaf = 120 + iota
	intakeReconcileRetirement
	intakeReconcileHiddenRoots
	intakeReconcileCapture
	intakeReconcileLease
	intakeReconcileArrival
)

type intakeReconcileFailure struct {
	leaf  intakeReconcileLeaf
	cause error
}

func (failure *intakeReconcileFailure) Error() string { return failure.cause.Error() }
func (failure *intakeReconcileFailure) Unwrap() error { return failure.cause }

// These statuses describe only the private intake monitor's actual failure
// phase. Every failure remains fatal; they grant no retry or lifecycle authority.
type intakeFailurePhase uint8

const (
	intakeFailurePoll intakeFailurePhase = 80 + iota
	intakeFailureControl
	intakeFailureMountInfo
	intakeFailureTopology
	intakeFailureReconcile
	intakeFailureHeartbeat
	intakeFailureWatchdog
)

type intakeMonitorFailure struct {
	phase intakeFailurePhase
	cause error
}

func (failure *intakeMonitorFailure) Error() string { return failure.cause.Error() }
func (failure *intakeMonitorFailure) Unwrap() error { return failure.cause }

// MountIntakeExitStatus is used only for the selected private intake role.
// Normal and unclassified outcomes preserve their existing zero/one statuses.
func MountIntakeExitStatus(err error) int {
	if err == nil {
		return 0
	}
	var failure *intakeMonitorFailure
	if errors.As(err, &failure) {
		var topology *mountTopologyRefusal
		if errors.As(failure.cause, &topology) && topology.predicate <= topologyCycle {
			switch failure.phase {
			case intakeFailureTopology:
				return 100 + int(topology.predicate)
			case intakeFailureReconcile:
				return 110 + int(topology.predicate)
			case intakeFailurePoll, intakeFailureControl, intakeFailureMountInfo, intakeFailureHeartbeat, intakeFailureWatchdog:
				// Other phases retain their typed fallback status below.
			}
		}
		var reconcile *intakeReconcileFailure
		if failure.phase == intakeFailureReconcile && errors.As(failure.cause, &reconcile) {
			switch reconcile.leaf {
			case intakeReconcileStructural, intakeReconcileRetirement, intakeReconcileHiddenRoots,
				intakeReconcileCapture, intakeReconcileLease, intakeReconcileArrival:
				return int(reconcile.leaf)
			}
		}

		switch failure.phase {
		case intakeFailurePoll, intakeFailureControl, intakeFailureMountInfo, intakeFailureTopology,
			intakeFailureReconcile, intakeFailureHeartbeat, intakeFailureWatchdog:
			return int(failure.phase)
		}
	}
	return 1
}
