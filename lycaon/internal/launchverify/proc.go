package launchverify

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/osprocess"
)

// Snapshot is one process's identity at an instant: its pid names the same
// process only while its start time is unchanged.
type Snapshot struct {
	PID       int
	PPID      int
	StartTime uint64
	TracerPID int
	// UIDs are the real, effective, saved and filesystem user identities.
	UIDs [4]uint32
}

// Same reports whether other still names the process this snapshot saw.
func (s Snapshot) Same(other Snapshot) bool {
	return s.PID == other.PID && s.StartTime == other.StartTime && s.PPID == other.PPID && s.UIDs == other.UIDs
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
	record, err := osprocess.ParseLinuxStat(stat)
	if err != nil {
		return Snapshot{}, err
	}
	if record.PID != pid {
		return Snapshot{}, fmt.Errorf("stat names pid %d", record.PID)
	}
	snap := Snapshot{PID: record.PID, PPID: record.ParentPID, StartTime: record.StartTime}
	status, err := os.ReadFile(v.procPath(pid, "status"))
	if err != nil {
		return Snapshot{}, err
	}
	snap.UIDs, err = parseStatusUIDs(status)
	if err != nil {
		return Snapshot{}, err
	}
	snap.TracerPID, err = parseTracerPID(status)
	if err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}

func parseStatusUIDs(status []byte) ([4]uint32, error) {
	var values [4]uint32
	found := false
	for _, line := range strings.Split(string(status), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		if found {
			return values, errors.New("status has duplicate Uid facts")
		}
		found = true
		fields := strings.Fields(strings.TrimPrefix(line, "Uid:"))
		if len(fields) != len(values) {
			return values, errors.New("status Uid fact must contain four identities")
		}
		for i, field := range fields {
			value, err := strconv.ParseUint(field, 10, 32)
			if err != nil {
				return values, fmt.Errorf("status uid: %w", err)
			}
			values[i] = uint32(value)
		}
	}
	if !found {
		return values, errors.New("status has no Uid fact")
	}
	return values, nil
}

func (s Snapshot) matchesUID(uid int) bool {
	if uid < 0 || uint64(uid) >= uint64(^uint32(0)) {
		return false
	}
	for _, actual := range s.UIDs {
		if actual != uint32(uid) {
			return false
		}
	}
	return true
}
