//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const GuardianArgument = "--protected-guardian"

var errProtectedDesktopEnded = errors.New("protected desktop ended")

type protectedGuardian struct {
	roots                                                         guardianRoots
	reaped                                                        [3]bool
	supervisor                                                    int
	caller                                                        Identity
	children, proc, control, health, lifetime, viewLifetime, gate *os.File
	retired                                                       bool
	failed                                                        bool
	failureNotified                                               bool
	status                                                        int
	diagnosticBirths                                              [4]int64
	nextHealth                                                    uint64
	lastHealth                                                    time.Time
}

// RunGuardian begins only after the fixed single-thread transition has ended
// initial-root authority. A fresh Go runtime inherits caller credentials and
// empty capability sets; the root-owned namespace shields its exec window.
func RunGuardian() (int, error) {
	runtime.LockOSThread()
	if unix.Gettid() != os.Getpid() {
		return 1, errors.New("guardian must execute on the process leader")
	}
	if err := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return 1, err
	}
	frame := os.NewFile(5, "sealed-entry-spec")
	spec, err := readEntry(frame)
	if err != nil {
		return 1, err
	}
	proof := os.NewFile(10, "sealed-guardian-transition")
	if err := authenticateGuardianTransition(proof, spec); err != nil {
		return 1, err
	}
	if err := sealGuardianIdentity(spec); err != nil {
		return 1, err
	}
	var subreaper int32
	// #nosec G103 -- PR_GET_CHILD_SUBREAPER writes one kernel int into this live int32; x/sys has no typed pointer-output owner.
	if err := unix.Prctl(unix.PR_GET_CHILD_SUBREAPER, uintptr(unsafe.Pointer(&subreaper)), 0, 0, 0); err != nil || subreaper != 1 {
		return 1, errors.New("guardian is not the protected lineage subreaper")
	}
	if os.Getppid() != spec.SupervisorPID {
		return 1, errors.New("guardian supervisor differs from launch")
	}
	rootFrame := os.NewFile(11, "sealed-guardian-roots")
	roots, err := readGuardianRoots(rootFrame)
	if err != nil {
		return 1, err
	}
	if roots.stage != os.Getpid() {
		return 1, errors.New("guardian frame belongs to another process")
	}
	path, err := os.Readlink(mountDescriptorPath(13))
	if err != nil || path != fmt.Sprintf("/proc/%d/task/%d/children", roots.stage, roots.leader) {
		return 1, errors.New("guardian children descriptor does not name the original process leader")
	}
	for index, pid := range roots.pids {
		if err := verifyGuardianPIDFD(15+index, pid); err != nil {
			return 1, err
		}
	}
	_ = frame.Close()
	_ = proof.Close()
	_ = rootFrame.Close()
	state := &protectedGuardian{roots: roots, supervisor: spec.SupervisorPID, caller: Identity{UID: spec.UID, GID: spec.GID, Groups: spec.Groups},
		children: os.NewFile(13, "protected-lineage-children"), proc: os.NewFile(14, "protected-process-directory"),
		control: os.NewFile(12, "guardian-bootstrap"), health: os.NewFile(7, "mount-view-health"),
		lifetime: os.NewFile(6, "generation-lifetime"), viewLifetime: os.NewFile(18, "mount-view-lifetime"), gate: os.NewFile(19, "desktop-admission"), nextHealth: roots.sequence, lastHealth: time.Now()}
	state.diagnosticBirths = state.captureDiagnosticBirths()
	return state.run(), nil
}

func (state *protectedGuardian) run() int {
	defer func() {
		_ = state.lifetime.Close()
		_ = state.children.Close()
		_ = state.proc.Close()
		_ = state.control.Close()
		_ = state.health.Close()
		_ = state.viewLifetime.Close()
		_ = state.gate.Close()
		for fd := 15; fd <= 17; fd++ {
			_ = unix.Close(fd)
		}
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, unix.SIGTERM, unix.SIGINT, unix.SIGHUP)
	defer signal.Stop(signals)
	if err := state.admit(); err != nil {
		// Diagnostics start only after admission; they cannot substitute its Ready/Admit handshake.
		state.retired = true
		state.failed = true
	}
	for {
		noChildren, err := state.reap()
		if err != nil {
			state.reportRetirement(guardianRetirement{Cause: guardianWaitFailed, RoleIndex: -1})
			state.retired = true
			state.failed = true
		}
		if noChildren {
			if state.registeredSettled() {
				if state.failed && state.status == 0 {
					state.status = 1
				}
				status := state.status
				if status < 0 || status > 255 {
					return 1
				}
				_ = sendGuardianMessage(int(state.control.Fd()), guardianSettled, uint32(status))
				return status
			}
			// ECHILD alone is insufficient if any authenticated role still lives.
			state.reportRetirement(guardianRetirement{Cause: guardianUnsettledChildren, RoleIndex: -1})
			state.retired = true
			state.failed = true
		}
		select {
		case signal := <-signals:
			if number, ok := signal.(unix.Signal); ok {
				state.reportRetirement(guardianRetirement{Cause: guardianSignalReceived, RoleIndex: -1, Signal: int(number)})
			}
			state.retired = true
			state.failed = true
		default:
		}
		if state.retired {
			if state.failed && !state.failureNotified {
				if sendGuardianMessage(int(state.control.Fd()), guardianFailed, 1) == nil {
					state.failureNotified = true
				}
			}
			_ = state.gate.Close()
			_ = state.viewLifetime.Close()
			state.terminateRoots()
			pids, censusErr := guardianDirectChildren(state.children)
			if censusErr == nil {
				for _, pid := range pids {
					if err := terminateOwnedLaunchChild(state.proc, pid, []uint32{state.caller.UID}); err != nil {
						state.failed = true
					}
				}
			} else {
				state.failed = true
			}
			// Never release lifetime custody on a deadline or successful kill alone.
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if observation, err := state.observe(); err != nil {
			state.reportRetirement(guardianRetirement{Cause: observation.Cause, RoleIndex: observation.RoleIndex, Observation: &observation})
			state.retired = true
			if !errors.Is(err, errProtectedDesktopEnded) {
				state.failed = true
				_, _ = fmt.Fprintf(os.Stderr, "Painted Wolf Code: protected launch guardian stopped: %v\n", err)
			}
		}
	}
}

func (state *protectedGuardian) admit() error {
	controlPoll, err := guardianPollDescriptor(state.control.Fd())
	if err != nil {
		return err
	}
	if !state.registeredAlive() {
		return errors.New("protected role exited before guardian admission")
	}
	if err := guardianProtocolSocket(int(state.control.Fd())); err != nil {
		return err
	}
	if err := unix.SetNonblock(int(state.control.Fd()), true); err != nil {
		return err
	}
	if err := sendGuardianMessage(int(state.control.Fd()), guardianReady, 0); err != nil {
		return err
	}
	deadline := time.Now().Add(mountSourceSetupLimit)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return errors.New("guardian admission deadline exceeded")
		}
		polls := []unix.PollFd{{Fd: controlPoll, Events: unix.POLLIN}}
		_, err := unix.Poll(polls, int((remaining+time.Millisecond-1)/time.Millisecond))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if polls[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return errors.New("bootstrap ended before guardian admission")
		}
		if polls[0].Revents&unix.POLLIN == 0 {
			continue
		}
		notice, receiveErr := receiveGuardianNotice(int(state.control.Fd()), state.supervisor, state.caller)
		err = receiveErr
		if err == nil && notice.phase != guardianAdmit {
			err = errors.New("bootstrap did not admit protected desktop")
		}
		if errors.Is(err, unix.EAGAIN) {
			continue
		}
		if err != nil {
			return err
		}
		if !state.registeredAlive() {
			return errors.New("protected role exited during guardian admission")
		}
		if _, err := state.gate.Write([]byte{1}); err != nil {
			return err
		}
		return state.gate.Close()
	}
}

func (state *protectedGuardian) observe() (guardianObservation, error) {
	observation := guardianObservation{Cause: guardianPollFailed, RoleIndex: -1}
	health, err := guardianPollDescriptor(state.health.Fd())
	if err != nil {
		return observation, err
	}
	polls := []unix.PollFd{{Fd: health, Events: unix.POLLIN}}
	for fd := 15; fd <= 17; fd++ {
		polls = append(polls, unix.PollFd{Fd: int32(fd), Events: unix.POLLIN})
	}
	_, err = unix.Poll(polls, 100)
	for index, poll := range polls {
		observation.Poll[index] = poll.Revents
	}
	if errors.Is(err, unix.EINTR) {
		return observation, nil
	}
	if err != nil {
		return observation, err
	}
	for index, poll := range polls {
		if poll.Revents == 0 {
			continue
		}
		if index == 1 && poll.Revents&unix.POLLIN != 0 {
			observation.Cause, observation.RoleIndex = guardianDesktopReady, 0
			return observation, errProtectedDesktopEnded
		}
		if index != 0 || poll.Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			observation.Cause = guardianHealthEnded
			if index != 0 {
				observation.Cause, observation.RoleIndex = guardianRoleReady, index-1
			}
			return observation, errors.New("protected role or health channel ended")
		}
		message, fd, err := receiveMountTransfer(int(state.health.Fd()))
		if err == nil {
			observation.Frame = &guardianHealthFact{Kind: message.kind, Sequence: message.sequence, Descriptor: fd >= 0}
		}
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		if err != nil || fd >= 0 || message != (mountTransfer{kind: mountTransferReady, sequence: state.nextHealth}) || state.nextHealth == ^uint64(0) {
			observation.Cause = guardianHealthRejected
			return observation, errors.New("invalid private mount health sequence")
		}
		state.nextHealth++
		state.lastHealth = time.Now()
	}
	if time.Since(state.lastHealth) > mountSourceSetupLimit {
		observation.Cause = guardianHealthExpired
		return observation, errors.New("private mount health deadline exceeded")
	}
	return observation, nil
}

func (state *protectedGuardian) reap() (bool, error) {
	for {
		var status unix.WaitStatus
		pid, err := unix.Wait4(-1, &status, unix.WNOHANG|unix.WALL, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.ECHILD) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if pid == 0 {
			return false, nil
		}
		state.recordExit(pid, status)
	}
}

// A pidfd retains the registered task, not its numeric PID allocation. Once
// its wait result is consumed, a later adopted child can reuse that number.
func (state *protectedGuardian) recordExit(pid int, status unix.WaitStatus) {
	for index, root := range state.roots.pids {
		if pid != root || state.reaped[index] {
			continue
		}
		state.reaped[index] = true
		waitStatus := uint32(status)
		state.reportRetirement(guardianRetirement{Cause: guardianRootExited, RoleIndex: index, WaitStatus: &waitStatus})
		if index != 0 && !state.retired {
			state.failed = true
		}
		state.retired = true
		if index == 0 && status.Exited() {
			state.status = status.ExitStatus()
		} else if index == 0 && status.Signaled() {
			state.status = 128 + int(status.Signal())
		}
	}
}

func (state *protectedGuardian) registeredAlive() bool {
	polls := []unix.PollFd{{Fd: 15, Events: unix.POLLIN}, {Fd: 16, Events: unix.POLLIN}, {Fd: 17, Events: unix.POLLIN}}
	if _, err := unix.Poll(polls, 0); err != nil {
		return false
	}
	for _, poll := range polls {
		if poll.Revents != 0 {
			return false
		}
	}
	return true
}

func (state *protectedGuardian) registeredSettled() bool {
	polls := []unix.PollFd{{Fd: 15, Events: unix.POLLIN}, {Fd: 16, Events: unix.POLLIN}, {Fd: 17, Events: unix.POLLIN}}
	if _, err := unix.Poll(polls, 0); err != nil {
		return false
	}
	for _, poll := range polls {
		if poll.Revents&unix.POLLIN == 0 || poll.Revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
			return false
		}
	}
	return true
}

func (state *protectedGuardian) terminateRoots() {
	for fd := 15; fd <= 17; fd++ {
		if err := unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
			state.failed = true
		}
	}
}
