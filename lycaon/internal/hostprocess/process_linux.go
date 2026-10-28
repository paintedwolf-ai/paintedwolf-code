//go:build linux

package hostprocess

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

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
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		return Process{}, err
	}
	executable, _ := os.Readlink(path + "/exe")
	started, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return Process{}, err
	}
	stats := linuxProcessStats(fields, started)
	return Process{StartOrder: started, PID: pid, ParentPID: parent, UID: stat.Uid, Name: string(raw[start+1 : end]), Executable: executable, Instance: fields[19], Stats: stats}, nil
}

var linuxBootTime = sync.OnceValue(func() time.Time {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if value, ok := strings.CutPrefix(line, "btime "); ok {
			seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err == nil {
				return time.Unix(seconds, 0)
			}
		}
	}
	return time.Time{}
})

func linuxProcessStats(fields []string, started uint64) ProcessStats {
	if len(fields) < 22 {
		return ProcessStats{}
	}
	user, e1 := strconv.ParseUint(fields[11], 10, 64)
	system, e2 := strconv.ParseUint(fields[12], 10, 64)
	virtual, e3 := strconv.ParseUint(fields[20], 10, 64)
	resident, e4 := strconv.ParseUint(fields[21], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return ProcessStats{}
	}
	stats := ProcessStats{Available: true, State: fields[0], TTY: "?", VirtualBytes: virtual,
		ResidentBytes: resident * uint64(os.Getpagesize()), CPUTime: time.Duration(user+system) * time.Second / 100}
	if boot := linuxBootTime(); !boot.IsZero() {
		stats.Started = boot.Add(time.Duration(started) * time.Second / 100)
	}
	if tty, err := strconv.ParseUint(fields[4], 10, 64); err == nil && tty != 0 {
		major := (tty >> 8) & 0xfff
		minor := (tty & 0xff) | ((tty >> 12) & 0xfff00)
		switch {
		case major >= 136 && major <= 143:
			stats.TTY = "pts/" + strconv.FormatUint(minor, 10)
		case major == 4:
			stats.TTY = "tty" + strconv.FormatUint(minor, 10)
		default:
			stats.TTY = fmt.Sprintf("%d:%d", major, minor)
		}
	}
	return stats
}

func PhysicalMemoryBytes() uint64 {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if value, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			fields := strings.Fields(value)
			if len(fields) > 0 {
				n, _ := strconv.ParseUint(fields[0], 10, 64)
				return n * 1024
			}
		}
	}
	return 0
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

func readCommandLine(pid int) string {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(raw) == 0 {
		return ""
	}
	raw = bytes.TrimRight(raw, "\x00")
	if len(raw) == 0 {
		return ""
	}
	parts := bytes.Split(raw, []byte{0})
	args := make([]string, 0, len(parts))
	for _, p := range parts {
		args = append(args, string(p))
	}
	return strings.Join(args, " ")
}

func processExistsFallback(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
