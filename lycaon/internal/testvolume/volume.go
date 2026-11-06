//go:build darwin || linux

// Package testvolume provides real second filesystems for placement tests: a
// disk image on macOS, and a tmpfs in a private mount namespace on Linux.
package testvolume

import (
	"os"
	"path/filepath"
	"testing"
)

// Volume is one mounted filesystem. Root is its mount point.
type Volume struct {
	Root    string
	eject   func(t *testing.T)
	remount func(t *testing.T)
}

// Kind selects what the volume can do.
type Kind int

const (
	// Clonable is APFS on macOS and the volume PW_TEST_REFLINK_VOLUME names on Linux.
	Clonable Kind = iota
	// NoClone cannot clone: HFS+ on macOS, tmpfs on Linux.
	NoClone
	// Movable is clone-capable on macOS and a tmpfs on Linux, for tests of the
	// volume going away. Linux tests inject the clone because tmpfs has none.
	Movable
)

// ReflinkVolumeEnv names a writable directory on a second reflink-capable Linux
// volume. No such volume can be made without elevation.
const ReflinkVolumeEnv = "PW_TEST_REFLINK_VOLUME"

// Eject makes the volume absent while keeping its contents.
func (v *Volume) Eject(t *testing.T) {
	t.Helper()
	v.eject(t)
}

// Remount brings the volume back at the same mount point.
func (v *Volume) Remount(t *testing.T) {
	t.Helper()
	v.remount(t)
}

// Dir creates a directory on the volume.
func (v *Volume) Dir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(v.Root, name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("create volume directory: %v", err)
	}
	return dir
}
