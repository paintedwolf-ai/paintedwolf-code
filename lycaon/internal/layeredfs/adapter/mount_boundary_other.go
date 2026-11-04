//go:build !unix

package adapter

// MountBoundaryPresent has no mounted adapter to observe on this platform.
func MountBoundaryPresent(string) (bool, error) { return false, nil }
