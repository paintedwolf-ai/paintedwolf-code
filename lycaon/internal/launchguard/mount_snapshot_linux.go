//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"slices"

	"golang.org/x/sys/unix"
)

// Pins retain namespace-local IDs while startup compares the pre-intake view
// with the privatized view. An ID cannot be recycled while its mount is held.
// Hidden stacked mounts have no path handle; their visible covering mount and
// complete mountinfo topology must remain unchanged through the handshake.
type mountSnapshot struct {
	entries []mountEntry
	pins    map[int]int
}

func pinMountSnapshot(entries []mountEntry) (snapshot *mountSnapshot, err error) {
	snapshot = &mountSnapshot{entries: slices.Clone(entries), pins: make(map[int]int)}
	defer func() {
		if err != nil {
			snapshot.close()
		}
	}()
	seen := make(map[int]bool, len(entries))
	for _, entry := range entries {
		if entry.id <= 0 || entry.parent < 0 {
			return nil, errors.New("invalid mount snapshot identity")
		}
		if seen[entry.id] {
			return nil, errors.New("duplicate mount snapshot identity")
		}
		seen[entry.id] = true
		if isShadowedMount(entry, entries) {
			continue
		}
		fd, openErr := openMountPoint(entry.mountPoint)
		if openErr != nil {
			return nil, fmt.Errorf("pin mount %q: %w", entry.mountPoint, openErr)
		}
		id, statErr := mountID(fd)
		if statErr != nil || id != uint64(entry.id) {
			_ = unix.Close(fd)
			return nil, errors.New("mount changed during snapshot pinning")
		}
		snapshot.pins[entry.id] = fd
	}
	return snapshot, nil
}

func (snapshot *mountSnapshot) close() {
	for id, fd := range snapshot.pins {
		_ = unix.Close(fd)
		delete(snapshot.pins, id)
	}
}

// Optional propagation tags intentionally are not in mountEntry: making the
// view private changes those tags, but must change none of these facts.
func (snapshot *mountSnapshot) unchanged(entries []mountEntry) bool {
	if len(entries) != len(snapshot.entries) {
		return false
	}
	byID := make(map[int]mountEntry, len(entries))
	for _, entry := range entries {
		if _, exists := byID[entry.id]; exists {
			return false
		}
		byID[entry.id] = entry
	}
	for _, expected := range snapshot.entries {
		actual, exists := byID[expected.id]
		actual.shared, actual.master, actual.propagateFrom, actual.unbindable = 0, 0, 0, false
		expected.shared, expected.master, expected.propagateFrom, expected.unbindable = 0, 0, 0, false
		if !exists || actual != expected {
			return false
		}
		if fd, pinned := snapshot.pins[expected.id]; pinned {
			id, err := mountID(fd)
			if err != nil || id != uint64(expected.id) {
				return false
			}
		}
	}
	return true
}

func openMountPoint(name string) (int, error) {
	if err := (mountTransfer{kind: mountTransferAdd, sequence: 1, id: 1, path: name}).validate(); err != nil {
		return -1, unix.EINVAL
	}
	return unix.Openat2(unix.AT_FDCWD, name, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
}

func mountID(fd int) (uint64, error) {
	var stat unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &stat); err != nil {
		return 0, err
	}
	if stat.Mask&unix.STATX_MNT_ID == 0 || stat.Mnt_id == 0 {
		return 0, errors.New("kernel did not report mount identity")
	}
	return stat.Mnt_id, nil
}
