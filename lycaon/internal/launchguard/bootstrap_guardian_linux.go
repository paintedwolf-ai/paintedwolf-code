//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func awaitGuardianHandoff(control *os.File, stagePID int, stageFD int, caller Identity, origin *os.File) error {
	if err := unix.SetNonblock(int(control.Fd()), true); err != nil {
		return err
	}
	deadline := time.Now().Add(mountSourceSetupLimit)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return errors.New("protected setup did not hand off before its deadline")
		}
		polls := []unix.PollFd{{Fd: int32(control.Fd()), Events: unix.POLLIN}, {Fd: int32(stageFD), Events: unix.POLLIN}}
		_, err := unix.Poll(polls, int((remaining+time.Millisecond-1)/time.Millisecond))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if polls[0].Revents&unix.POLLIN != 0 {
			notice, err := receiveGuardianNotice(int(control.Fd()), stagePID, caller)
			if errors.Is(err, unix.EAGAIN) {
				continue
			}
			if err != nil {
				return err
			}
			if notice.phase != guardianReady {
				return errors.New("protected stage did not hand off a ready guardian")
			}
			if err := verifyGuardianProfile(stagePID, caller, origin); err != nil {
				return err
			}
			alive := []unix.PollFd{{Fd: int32(stageFD), Events: unix.POLLIN}}
			if _, err := unix.Poll(alive, 0); err != nil || alive[0].Revents != 0 {
				return errors.New("guardian exited during profile verification")
			}
			return nil
		}
		if polls[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 || polls[1].Revents != 0 {
			return errors.New("protected stage ended before caller-only handoff")
		}
	}
}

// UI admission has not occurred, so this subreaper owns only fixed setup
// roles. Stop the sole creator first; after it exits all remaining children
// are directly owned. No ordinary broker request could have run behind it.
func retireBootstrapSetup(stageFD int, children, proc *os.File, caller Identity, transition *os.File) {
	noticePID, notice, _ := startBootstrapNotice(transition, caller)
	noticeDeadline := time.Now().Add(time.Second)
	if notice != nil {
		defer func() { _ = notice.Close() }()
	}
	_ = unix.PidfdSendSignal(stageFD, unix.SIGKILL, nil, 0)
	stageExited := false
	for {
		expireBootstrapNotice(notice, noticeDeadline)
		polls := []unix.PollFd{{Fd: int32(stageFD), Events: unix.POLLIN}}
		if _, err := unix.Poll(polls, 0); err == nil && polls[0].Revents&unix.POLLIN != 0 {
			stageExited = true
		}
		if stageExited {
			if pids, err := guardianDirectChildren(children); err == nil {
				for _, pid := range pids {
					if pid == noticePID && time.Now().Before(noticeDeadline) {
						continue
					}
					_ = terminateOwnedLaunchChild(proc, pid, []uint32{0, caller.UID})
				}
			}
		}
		for {
			var status unix.WaitStatus
			pid, err := unix.Wait4(-1, &status, unix.WNOHANG|unix.WALL, nil)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if errors.Is(err, unix.ECHILD) {
				return
			}
			if err != nil || pid == 0 {
				break
			}
		}
		// Unsettled kernel teardown keeps pre-admission generation custody.
		// A kill request or elapsed deadline is never reported as settlement.
		time.Sleep(10 * time.Millisecond)
	}
}

func dropBootstrapSupervisor(caller Identity) error {
	for capability := uintptr(0); capability < 64; capability++ {
		if err := prctlAllThreads(unix.PR_CAPBSET_DROP, capability, 0, 0, 0); err != nil {
			if errors.Is(err, unix.EINVAL) {
				break
			}
			return err
		}
	}
	if err := dropPrivileges(caller); err != nil {
		return err
	}
	return prctlAllThreads(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)
}

// After handoff this supervisor has no privilege. Only an authenticated
// settled notice plus actual child reaping releases generation custody.
// Unexpected guardian death is visible but cannot prove escaped descendants
// settled; custody stays held instead of treating its exit as successful cleanup.
func superviseGuardianLaunch(lifetime, control *os.File, stagePID int, stageFD int, caller Identity) int {
	settled, stageExited, noChildren, notified := false, false, false, false
	controlClosed := false
	status := 0
	for {
		socket := int32(control.Fd())
		if controlClosed {
			socket = -1
		}
		polls := []unix.PollFd{{Fd: socket, Events: unix.POLLIN}}
		_, pollErr := unix.Poll(polls, 100)
		if pollErr != nil && !errors.Is(pollErr, unix.EINTR) {
			notified = notifyGuardianFailure(notified)
		}
		if polls[0].Revents&unix.POLLIN != 0 {
			notice, err := receiveGuardianNotice(int(control.Fd()), stagePID, caller)
			if err != nil {
				if !errors.Is(err, unix.EAGAIN) {
					notified = notifyGuardianFailure(notified)
					controlClosed = true
					_ = unix.PidfdSendSignal(stageFD, unix.SIGTERM, nil, 0)
				}
			} else {
				switch notice.phase {
				case guardianFailed:
					notified = notifyGuardianFailure(notified)
				case guardianSettled:
					settled = true
					status = int(notice.status)
				default:
					notified = notifyGuardianFailure(notified)
					_ = unix.PidfdSendSignal(stageFD, unix.SIGTERM, nil, 0)
				}
			}
		}
		if polls[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 && polls[0].Revents&unix.POLLIN == 0 {
			controlClosed = true
		}
		for {
			var wait unix.WaitStatus
			pid, err := unix.Wait4(-1, &wait, unix.WNOHANG|unix.WALL, nil)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if errors.Is(err, unix.ECHILD) {
				noChildren = true
				break
			}
			if err != nil {
				notified = notifyGuardianFailure(notified)
				break
			}
			if pid == 0 {
				break
			}
			if pid == stagePID {
				stageExited = true
			}
		}
		if stageExited && !settled {
			notified = notifyGuardianFailure(notified)
		}
		if settled && stageExited && noChildren {
			_ = lifetime.Close()
			_ = control.Close()
			return status
		}
	}
}

func notifyGuardianFailure(already bool) bool {
	if !already {
		_, _ = fmt.Fprintln(os.Stderr, "Painted Wolf Code: protected launch stopped. Waiting for its processes to exit.")
	}
	return true
}
