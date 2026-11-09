//go:build !linux && !darwin

package conformance

// statfsFilesystemType has no portable probe on this platform; callers must
// report "unknown" rather than guessing (#1667).
func statfsFilesystemType(path string) (string, bool) {
	return "", false
}
