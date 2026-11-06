// Package testvolume provides real second filesystems for placement tests: a
// disk image on macOS, and a tmpfs in a private mount namespace on Linux.
package testvolume

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Volume is one mounted filesystem. Root is its mount point.
type Volume struct {
	Root    string
	image   string
	scope   string
	shared  bool
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

// Dir creates a directory on the volume. A shared volume scopes it to the test.
func (v *Volume) Dir(t *testing.T, name string) string {
	t.Helper()
	base := v.Root
	if v.scope != "" {
		base = v.scope
	}
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("create volume directory: %v", err)
	}
	return dir
}

// Shared returns a directory on a second filesystem that the test binary mounts
// once per kind and hands to every test as its own scoped directory. Tests that
// eject, remount, or re-own the volume need New instead.
func Shared(t *testing.T, kind Kind) (*Volume, bool) {
	t.Helper()
	v, ran := sharedVolume(t, kind)
	if ran || v == nil {
		return v, ran
	}
	scope := filepath.Join(v.Root, sharedScopeName(t))
	if err := os.MkdirAll(scope, 0o750); err != nil {
		t.Fatalf("create shared volume scope: %v", err)
	}
	// The image is discarded whole, so a scope that will not remove is left for it.
	t.Cleanup(func() { _ = os.RemoveAll(scope) })
	return &Volume{Root: v.Root, image: v.image, scope: scope, shared: true, eject: sharedLifecycleFatal, remount: sharedLifecycleFatal}, false
}

var sharedScopeCounter atomic.Uint64

// sharedScopeName names one test's directory on a shared volume. The counter
// keeps repeated runs apart, and the name keeps to characters every filesystem allows.
func sharedScopeName(t *testing.T) string {
	name := t.Name()
	if len(name) > 64 {
		name = name[:64]
	}
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, name)
	return fmt.Sprintf("t%03d-%s", sharedScopeCounter.Add(1), name)
}

func sharedLifecycleFatal(t *testing.T) {
	t.Helper()
	t.Fatal("the shared test volume serves the whole test binary; tests that eject or remount it need testvolume.New")
}

// SweepStrays detaches pwplacement test volumes left by finished or crashed test
// runs and removes their images. Volumes whose owning process still runs are left
// alone, so concurrently running test binaries never disturb each other.
func SweepStrays() {
	sweepStrays()
}
