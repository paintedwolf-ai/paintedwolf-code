//go:build linux

package exec

import (
	"bytes"
	"os"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

type descendant struct {
	pid    int
	parent int
	start  uint64
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
		raw, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		end := bytes.LastIndexByte(raw, ')')
		if end < 0 {
			continue
		}
		fields := bytes.Fields(raw[end+1:])
		if len(fields) < 20 {
			continue
		}
		parent, err := strconv.Atoi(string(fields[1]))
		if err != nil {
			continue
		}
		start, err := strconv.ParseUint(string(fields[19]), 10, 64)
		if err != nil {
			continue
		}
		all[pid] = descendant{pid: pid, parent: parent, start: start}
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
		raw, err := os.ReadFile("/proc/" + strconv.Itoa(child.pid) + "/stat")
		if err == nil {
			end := bytes.LastIndexByte(raw, ')')
			if end >= 0 {
				fields := bytes.Fields(raw[end+1:])
				if len(fields) >= 20 {
					start, err := strconv.ParseUint(string(fields[19]), 10, 64)
					if err == nil && start == child.start && descendantOf(child.pid, root) {
						_ = unix.PidfdSendSignal(fd, sig, nil, 0)
					}
				}
			}
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
		raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return false
		}
		end := bytes.LastIndexByte(raw, ')')
		if end < 0 {
			return false
		}
		fields := bytes.Fields(raw[end+1:])
		if len(fields) < 2 {
			return false
		}
		pid, err = strconv.Atoi(string(fields[1]))
		if err != nil {
			return false
		}
	}
	return false
}
