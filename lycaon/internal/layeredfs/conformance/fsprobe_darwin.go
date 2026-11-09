//go:build darwin

package conformance

import "syscall"

// statfsFilesystemType probes the real backing filesystem type via statfs(2).
// macOS reports a textual fstype (e.g. "apfs"); the bool is false when the
// probe fails, and callers must report "unknown" rather than guessing (#1667).
func statfsFilesystemType(path string) (string, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return "", false
	}
	return int8String(st.Fstypename[:]), true
}

func int8String(b []int8) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c == 0 {
			break
		}
		out = append(out, byte(c))
	}
	return string(out)
}
