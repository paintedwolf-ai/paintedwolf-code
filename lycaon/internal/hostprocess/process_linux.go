//go:build linux

package hostprocess

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/osprocess"
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
	record, err := osprocess.ParseLinuxStat(raw)
	if err != nil || record.PID != pid {
		return Process{}, ErrStale
	}
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		return Process{}, err
	}
	executable, _ := os.Readlink(path + "/exe")
	return Process{PID: pid, ParentPID: record.ParentPID, UID: stat.Uid, Name: record.Command, Executable: executable, Instance: strconv.FormatUint(record.StartTime, 10)}, nil
}

func nativeSignal(kind signalKind) (unix.Signal, bool) {
	signal, ok := map[signalKind]unix.Signal{
		signalTerminate: unix.SIGTERM, signalKill: unix.SIGKILL, signalInterrupt: unix.SIGINT,
		signalHangup: unix.SIGHUP, signalStop: unix.SIGSTOP, signalContinue: unix.SIGCONT,
		signalUser1: unix.SIGUSR1, signalUser2: unix.SIGUSR2,
	}[kind]
	return signal, ok
}

func signalInstance(process Process, kind signalKind) error {
	signal, ok := nativeSignal(kind)
	if !ok {
		return ErrUnsupported
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
