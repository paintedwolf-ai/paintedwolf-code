//go:build linux

package exec

import (
	"bytes"
	"os"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type descendant struct {
	pid    int
	parent int
	start  uint64
}

// parseProcStat reads the parent and start time from a /proc/<pid>/stat line;
// the command name may itself contain ')' or spaces.
func parseProcStat(raw []byte) (parent int, start uint64, ok bool) {
	end := bytes.LastIndexByte(raw, ')')
	if end < 0 {
		return 0, 0, false
	}
	fields := bytes.Fields(raw[end+1:])
	if len(fields) < 20 {
		return 0, 0, false
	}
	parent, err := strconv.Atoi(string(fields[1]))
	if err != nil {
		return 0, 0, false
	}
	start, err = strconv.ParseUint(string(fields[19]), 10, 64)
	return parent, start, err == nil
}

func procStat(pid int) (parent int, start uint64, ok bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, false
	}
	return parseProcStat(raw)
}

func commandDescendants(root int) []descendant {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	all := make(map[int]descendant)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if parent, start, ok := procStat(pid); ok {
			all[pid] = descendant{pid: pid, parent: parent, start: start}
		}
	}
	owned := map[int]bool{root: true}
	var result []descendant
	for changed := true; changed; {
		changed = false
		for pid, child := range all {
			if !owned[pid] && owned[child.parent] {
				owned[pid] = true
				result = append(result, child)
				changed = true
			}
		}
	}
	return result
}

// pidfds keep a disappearing descendant's PID from naming an unrelated replacement.
func signalCommandDescendants(root int, sig unix.Signal) int {
	children := commandDescendants(root)
	for _, child := range children {
		fd, err := unix.PidfdOpen(child.pid, 0)
		if err != nil {
			continue
		}
		// Confirm the snapshot incarnation before signalling through the stable descriptor.
		if _, start, ok := procStat(child.pid); ok && start == child.start && descendantOf(child.pid, root) {
			_ = unix.PidfdSendSignal(fd, sig, nil, 0)
		}
		_ = unix.Close(fd)
	}
	return len(children)
}

func reapAdoptedChildren() {
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
	}
}

// descendantOf revalidates ancestry so a stale intermediate PID cannot confer lineage.
func descendantOf(pid, root int) bool {
	seen := map[int]bool{}
	for pid > 0 && !seen[pid] {
		if pid == root {
			return true
		}
		seen[pid] = true
		parent, _, ok := procStat(pid)
		if !ok {
			return false
		}
		pid = parent
	}
	return false
}

// supervision settles one primary command and every descendant of root.
type supervision struct {
	root     int
	finished <-chan error
	events   <-chan os.Signal
	// gone closes when the engine that launched the command disappears.
	gone  <-chan struct{}
	grace time.Duration
	reap  func()
}

// settle terminates the tree once the primary finishes or the engine asks,
// escalating to SIGKILL on request, on engine loss, or after the grace period.
func (s supervision) settle() error {
	var result error
	hard, settled := false, false
	gone := s.gone
	select {
	case result = <-s.finished:
		settled = true
	case sig := <-s.events:
		hard = sig == syscall.SIGUSR2
	case <-gone:
		hard, gone = true, nil
	}
	// Wait owns only the primary child; adopted children are reaped after it settles.
	deadline := time.Now().Add(s.grace)
	for {
		sig := unix.SIGTERM
		if hard || time.Now().After(deadline) {
			sig = unix.SIGKILL
		}
		remaining := signalCommandDescendants(s.root, sig)
		if !settled {
			select {
			case result = <-s.finished:
				settled = true
			default:
			}
		}
		if settled {
			s.reap()
			if remaining == 0 && len(commandDescendants(s.root)) == 0 {
				return result
			}
		}
		select {
		case sig := <-s.events:
			hard = hard || sig == syscall.SIGUSR2
		case <-gone:
			hard, gone = true, nil
		case <-time.After(10 * time.Millisecond):
		}
	}
}
