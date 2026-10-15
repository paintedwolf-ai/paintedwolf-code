//go:build !linux

package osprocess

// DisableDumps has no process-wide dumpability flag on this platform.
func DisableDumps() error { return nil }
