package launchverify

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Snapshot is one process's identity at an instant: its pid names the same
// process only while its start time is unchanged.
type Snapshot struct {
	PID       int
	PPID      int
	StartTime uint64
	// UID and EUID are the process's real and effective user ids.
	UID  int
	EUID int
}

// Same reports whether other still names the process this snapshot saw.
func (s Snapshot) Same(other Snapshot) bool {
	return s.PID == other.PID && s.StartTime == other.StartTime && s.PPID == other.PPID
}

func (v Verifier) procPath(pid int, name string) string {
	return filepath.Join(v.ProcRoot, strconv.Itoa(pid), name)
}

// snapshot reads the process's stat and status files.
func (v Verifier) snapshot(pid int) (Snapshot, error) {
	if pid <= 0 {
		return Snapshot{}, errors.New("pid is not positive")
	}
	stat, err := os.ReadFile(v.procPath(pid, "stat"))
	if err != nil {
		return Snapshot{}, err
	}
	snap, err := parseStat(stat)
	if err != nil {
		return Snapshot{}, err
	}
	if snap.PID != pid {
		return Snapshot{}, fmt.Errorf("stat names pid %d", snap.PID)
	}
	status, err := os.ReadFile(v.procPath(pid, "status"))
	if err != nil {
		return Snapshot{}, err
	}
	snap.UID, snap.EUID, err = parseStatusUIDs(status)
	if err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}

// parseStat reads pid, ppid, and start time from a stat line. The command
// name may contain spaces and parentheses, so fields start after its last
// closing parenthesis.
func parseStat(stat []byte) (Snapshot, error) {
	open := bytes.IndexByte(stat, '(')
	end := bytes.LastIndexByte(stat, ')')
	if open < 1 || end < open {
		return Snapshot{}, errors.New("stat has no command name")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(stat[:open])))
	if err != nil {
		return Snapshot{}, fmt.Errorf("stat pid: %w", err)
	}
	// Fields after the command: state, ppid, pgrp, session, tty, tpgid,
	// flags, minflt, cminflt, majflt, cmajflt, utime, stime, cutime, cstime,
	// priority, nice, threads, itrealvalue, starttime.
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) < 20 {
		return Snapshot{}, errors.New("stat is truncated")
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return Snapshot{}, fmt.Errorf("stat ppid: %w", err)
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return Snapshot{}, fmt.Errorf("stat start time: %w", err)
	}
	return Snapshot{PID: pid, PPID: ppid, StartTime: start}, nil
}

func parseStatusUIDs(status []byte) (uid, euid int, err error) {
	for _, line := range strings.Split(string(status), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "Uid:"))
		if len(fields) < 2 {
			return 0, 0, errors.New("status Uid line is truncated")
		}
		uid, err = strconv.Atoi(fields[0])
		if err != nil {
			return 0, 0, fmt.Errorf("status real uid: %w", err)
		}
		euid, err = strconv.Atoi(fields[1])
		if err != nil {
			return 0, 0, fmt.Errorf("status effective uid: %w", err)
		}
		return uid, euid, nil
	}
	return 0, 0, errors.New("status has no Uid line")
}
