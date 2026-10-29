//go:build linux

package osprocess

import (
	"os"
	"strconv"
)

// SessionMembers lists every process whose session id is sid, whatever
// process group it moved itself into. The leader is included while it exists.
func SessionMembers(sid int) []int {
	if sid <= 0 {
		return nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var members []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if sessionOf(pid) == sid {
			members = append(members, pid)
		}
	}
	return members
}

// sessionOf reads field 6 (session) of /proc/<pid>/stat, or 0 when the
// process is gone.
func sessionOf(pid int) int {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	record, err := ParseLinuxStat(raw)
	if err != nil || record.PID != pid {
		return 0
	}
	return record.SessionID
}
