//go:build linux

package osprocess

import (
	"bytes"
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
	// The command name may contain spaces and parentheses; fields resume after the last ')'.
	end := bytes.LastIndexByte(raw, ')')
	if end < 0 {
		return 0, false
	}
	// Field 22 (starttime) is the 20th field after the command name.
	fields := bytes.Fields(raw[end+1:])
	if len(fields) < 20 {
		return 0, false
	}
	start, err := strconv.ParseInt(string(fields[19]), 10, 64)
	if err != nil {
		return 0, false
	}
	return start, true
}
