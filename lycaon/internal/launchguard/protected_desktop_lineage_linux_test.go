//go:build linux && protected_desktop_artifact

package launchguard

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

type fixtureProcess struct {
	pid, parent, fd int
	start           uint64
}

// The pidfd is acquired before procfs observation and checked again afterwards;
// retaining a pidfd alone does not stop a reaped numeric PID being reused.
func pinDesktopFixtureProcess(pid, parent int) (fixtureProcess, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return fixtureProcess{}, err
	}
	process := fixtureProcess{pid: pid, fd: fd}
	process.parent, process.start, err = fixtureProcessBirth(pid)
	if err == nil && parent > 0 && process.parent != parent {
		err = errors.New("process is not this fixture's direct child")
	}
	if err == nil {
		err = process.current()
	}
	if err != nil {
		_ = unix.Close(fd)
		return fixtureProcess{}, err
	}
	return process, nil
}

func fixtureProcessBirth(pid int) (int, uint64, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, err
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return 0, 0, errors.New("malformed process stat")
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 {
		return 0, 0, errors.New("incomplete process stat")
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, err
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	return parent, start, err
}

func (process fixtureProcess) current() error {
	parent, start, err := fixtureProcessBirth(process.pid)
	if err != nil {
		return err
	}
	if parent != process.parent || start != process.start {
		return errors.New("process birth or parent changed")
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", process.fd))
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "Pid:") {
			pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Pid:")))
			if err == nil && pid == process.pid {
				return nil
			}
			return errors.New("pidfd no longer names observed process")
		}
	}
	return errors.New("pidfd has no kernel process identity")
}

func fixtureDescendsFrom(pid, ancestor int) error {
	seen := map[int]bool{}
	for pid > 1 && !seen[pid] {
		if pid == ancestor {
			return nil
		}
		seen[pid] = true
		parent, _, err := fixtureProcessBirth(pid)
		if err != nil {
			return err
		}
		pid = parent
	}
	return errors.New("observed process is outside the owned bootstrap lineage")
}

// Only this isolated driver is a subreaper. After directly started commands
// settle, every child remaining here is a kernel-adopted fixture descendant.
func settleDesktopLineage() error {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for {
			var status unix.WaitStatus
			pid, err := unix.Wait4(-1, &status, unix.WNOHANG, nil)
			if errors.Is(err, unix.ECHILD) {
				return nil
			}
			if err != nil && !errors.Is(err, unix.EINTR) {
				return err
			}
			if pid <= 0 {
				break
			}
		}
		tasks, err := os.ReadDir("/proc/self/task")
		if err != nil {
			return err
		}
		seen := map[int]bool{}
		for _, task := range tasks {
			raw, err := os.ReadFile(filepath.Join("/proc/self/task", task.Name(), "children"))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			for _, value := range strings.Fields(string(raw)) {
				pid, err := strconv.Atoi(value)
				if err != nil {
					return err
				}
				if seen[pid] {
					continue
				}
				seen[pid] = true
				process, err := pinDesktopFixtureProcess(pid, os.Getpid())
				if err != nil {
					continue
				} // Exit or adoption races are re-observed, never signalled by number.
				if process.current() == nil {
					err = unix.PidfdSendSignal(process.fd, unix.SIGKILL, nil, 0)
				}
				_ = unix.Close(process.fd)
				if err != nil && !errors.Is(err, unix.ESRCH) {
					return err
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("owned desktop descendants did not settle; preserve fixture for inspection")
}

func desktopFixtureProcessExited(fd int, timeout time.Duration) bool {
	descriptors := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		count, err := unix.Poll(descriptors, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false
		}
		if count > 0 && descriptors[0].Revents&unix.POLLIN != 0 {
			return true
		}
	}
	return false
}

func verifyDesktopLineageCleanup(t *testing.T) {
	t.Helper()
	command := exec.Command("/bin/sh", "-c", `sleep 60 </dev/null >/dev/null 2>&1 & printf '%s\n' "$!"`)
	output, err := command.Output()
	testutil.FailErr(t, "owned early-exit lineage control", err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
	testutil.FailErr(t, "adopted child identity", err)
	process, err := pinDesktopFixtureProcess(pid, os.Getpid())
	testutil.FailErr(t, "kernel adoption by fixture subreaper", err)
	defer unix.Close(process.fd)
	testutil.FailErr(t, "settle pre-health orphan control", settleDesktopLineage())
	if !desktopFixtureProcessExited(process.fd, time.Second) {
		t.Fatal("pre-health owned child survived cleanup")
	}
	t.Log("pre-health direct/adopted-child cleanup and kernel exit control passed")
}
