//go:build linux

package osprocess

import (
	"os"
	"strconv"
)

// StartTime identifies one incarnation of pid in clock ticks since boot.
// A reused pid reports a later start, so equality proves the same process.
func StartTime(pid int) (int64, bool) {
	if pid <= 0 {
		return 0, false
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	record, err := ParseLinuxStat(raw)
	if err != nil || record.PID != pid || record.StartTime > 1<<63-1 {
		return 0, false
	}
	return int64(record.StartTime), true
}

// CommandName returns the kernel-recorded executable name of pid.
func CommandName(pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	record, err := ParseLinuxStat(raw)
	if err != nil || record.PID != pid {
		return "", false
	}
	return record.Command, record.Command != ""
}
