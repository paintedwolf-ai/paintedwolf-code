//go:build !darwin && !linux

package osprocess

// SessionMembers has no session table to read on this platform.
func SessionMembers(int) []int { return nil }
