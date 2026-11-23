package fspath

import "strings"

// HasCreationDiscriminator reports whether identity carries birth time or an
// inode-generation handle rather than device and inode alone.
func HasCreationDiscriminator(identity string) bool {
	if identity == "" {
		return false
	}
	rest := identityWithoutDevice(identity)
	parts := strings.Split(rest, ":")
	switch len(parts) {
	case 3:
		// ino:btime_sec:btime_nsec on darwin and statx linux, or ino:h<type>:<hex> on handle linux.
		if strings.HasPrefix(parts[1], "h") {
			return len(parts[1]) > 1 && parts[2] != ""
		}
		return (parts[1] != "" && parts[1] != "0") || (parts[2] != "" && parts[2] != "0")
	default:
		return false
	}
}

// SameIncarnation reports whether recorded and current name the same entry
// creation across restarts. Bare device/inode pairs never match.
func SameIncarnation(recorded, current string) bool {
	if !HasCreationDiscriminator(recorded) || !HasCreationDiscriminator(current) {
		return false
	}
	return identityWithoutDevice(recorded) == identityWithoutDevice(current)
}
