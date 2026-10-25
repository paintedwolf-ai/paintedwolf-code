package osprocess

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
)

const maxLinuxStatBytes = 16 << 10

// ErrInvalidLinuxStat identifies an incomplete or malformed Linux process record.
var ErrInvalidLinuxStat = errors.New("invalid Linux process stat")

// LinuxStat contains kernel facts, without deciding process authority.
type LinuxStat struct {
	PID       int
	ParentPID int
	SessionID int
	StartTime uint64
	Command   string
}

// ParseLinuxStat reads the complete prefix through field 22. The command is
// opaque and may contain spaces, newlines and parentheses; later fields remain
// available to newer kernels without changing this fact reader.
func ParseLinuxStat(raw []byte) (LinuxStat, error) {
	if len(raw) == 0 || len(raw) > maxLinuxStatBytes {
		return LinuxStat{}, fmt.Errorf("%w: record size", ErrInvalidLinuxStat)
	}
	open, close := bytes.IndexByte(raw, '('), bytes.LastIndexByte(raw, ')')
	if open < 1 || close <= open || close+1 >= len(raw) || raw[open-1] != ' ' || raw[close+1] != ' ' {
		return LinuxStat{}, fmt.Errorf("%w: command delimiters", ErrInvalidLinuxStat)
	}
	fields := bytes.Fields(raw[close+1:])
	if len(fields) < 20 || len(fields[0]) != 1 {
		return LinuxStat{}, fmt.Errorf("%w: incomplete field 22 prefix", ErrInvalidLinuxStat)
	}
	pid, err := linuxStatNumber(bytes.TrimSpace(raw[:open]), strconv.IntSize-1, "pid")
	if err != nil {
		return LinuxStat{}, err
	}
	if pid == 0 {
		return LinuxStat{}, fmt.Errorf("%w: pid is not positive", ErrInvalidLinuxStat)
	}
	parent, err := linuxStatNumber(fields[1], strconv.IntSize-1, "parent pid")
	if err != nil {
		return LinuxStat{}, err
	}
	session, err := linuxStatNumber(fields[3], strconv.IntSize-1, "session id")
	if err != nil {
		return LinuxStat{}, err
	}
	start, err := linuxStatNumber(fields[19], 64, "start time")
	if err != nil {
		return LinuxStat{}, err
	}
	return LinuxStat{PID: int(pid), ParentPID: int(parent), SessionID: int(session), // #nosec G115 -- parsed with strconv.IntSize-1 bits
		StartTime: start, Command: string(raw[open+1 : close])}, nil
}

func linuxStatNumber(raw []byte, bits int, field string) (uint64, error) {
	value, err := strconv.ParseUint(string(raw), 10, bits)
	if err != nil {
		return 0, fmt.Errorf("%w: %s: %w", ErrInvalidLinuxStat, field, err)
	}
	return value, nil
}
