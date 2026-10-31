//go:build !darwin && !linux

package osprocess

// StartTime has no incarnation source on this platform, so no caller can prove a
// pid still names the process it recorded.
func StartTime(int) (int64, bool) { return 0, false }

// CommandName has no process table source on this platform.
func CommandName(int) (string, bool) { return "", false }
