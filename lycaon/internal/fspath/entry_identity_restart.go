package fspath

import "strings"

// SameEntryAcrossRestart compares a durably recorded identity with a live one
// ignoring the device number, which a reboot or remount may change. Compare two
// live entries with os.SameFile instead; this exists for what cannot be compared
// live, such as whether a retained file is the one a journal recorded.
func SameEntryAcrossRestart(recorded, current string) bool {
	if recorded == "" || current == "" {
		return false
	}
	was, is := identityWithoutDevice(recorded), identityWithoutDevice(current)
	if was == is {
		return true
	}
	// Releases recorded device and inode alone on a filesystem without birth time.
	// That record holds no generation to compare with today's file handle.
	return !strings.Contains(was, ":") && strings.HasPrefix(is, was+":h")
}
