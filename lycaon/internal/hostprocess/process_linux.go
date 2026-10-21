//go:build linux

package hostprocess

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func listPIDs() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := make([]int, 0, len(entries))
	for _, entry := range entries {
		if pid, err := strconv.Atoi(entry.Name()); err == nil && pid > 0 {
			out = append(out, pid)
		}
	}
	return out, nil
}

func inspect(pid int) (Process, error) {
	path := fmt.Sprintf("/proc/%d", pid)
	raw, err := os.ReadFile(path + "/stat")
	if err != nil {
		return Process{}, err
	}
	start, end := bytes.IndexByte(raw, '('), bytes.LastIndexByte(raw, ')')
	if start < 0 || end < start {
		return Process{}, ErrStale
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 {
		return Process{}, ErrStale
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return Process{}, err
	}
	uid, err := effectiveUID(path)
	if err != nil {
		return Process{}, err
	}
	executable, _ := os.Readlink(path + "/exe")
	return Process{PID: pid, ParentPID: parent, UID: uid, Name: string(raw[start+1 : end]), Executable: executable, Instance: fields[19]}, nil
}

// effectiveUID reads the process's effective uid from its status. The owner
// of the /proc entry is not that fact: a non-dumpable process's entry belongs
// to root whoever runs it.
func effectiveUID(path string) (uint32, error) {
	raw, err := os.ReadFile(path + "/status")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		ids, ok := strings.CutPrefix(line, "Uid:")
		if !ok {
			continue
		}
		fields := strings.Fields(ids)
		if len(fields) != 4 {
			return 0, ErrStale
		}
		uid, err := strconv.ParseUint(fields[1], 10, 32)
		if err != nil {
			return 0, ErrStale
		}
		return uint32(uid), nil
	}
	return 0, ErrStale
}

func signalInstance(process Process, name string) error {
	signal, ok := map[string]unix.Signal{"TERM": unix.SIGTERM, "KILL": unix.SIGKILL, "INT": unix.SIGINT, "HUP": unix.SIGHUP, "STOP": unix.SIGSTOP, "CONT": unix.SIGCONT, "USR1": unix.SIGUSR1, "USR2": unix.SIGUSR2}[name]
	if !ok {
		return fmt.Errorf("unsupported signal %q", name)
	}
	fd, err := unix.PidfdOpen(process.PID, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	current, err := inspect(process.PID)
	if err != nil {
		return err
	}
	if !sameInstance(current, process) {
		return ErrStale
	}
	return unix.PidfdSendSignal(fd, signal, nil, 0)
}
