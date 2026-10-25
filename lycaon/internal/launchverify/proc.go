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
	snap.UID, snap.EUID, err = parseStatusUIDs(status)
	if err != nil {
		return Snapshot{}, err
	}
	return snap, nil
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
