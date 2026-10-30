//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"math"
	"os"

	"golang.org/x/sys/unix"
)

// Structural selection is an exact opened-role set, closed over mount parents.
// A filesystem type, shared superblock or pathname prefix never selects peers
// or descendants. Every other inherited mount is removed before U_view exists.
func structuralMounts(policy GenerationPolicy, image *os.File, entries []mountEntry) (map[int]bool, error) {
	interpreter, err := programInterpreter(image)
	if err != nil {
		return nil, err
	}
	selected := make(map[int]bool)
	// The held generation is already bound at its logical root. Its store
	// path can be hidden beneath that bind when the store lives inside it.
	paths := append(moduleRoots(policy, interpreter), policy.LogicalInstallRoot, "/")
	for _, name := range paths {
		if err := selectStructuralObject(name, 0, selected); err != nil {
			return nil, err
		}
	}
	// These are fixed kernel interfaces, not filesystem classification rules.
	// Unknown child masks are restored as safe initial leases before readiness.
	for _, role := range []struct {
		path     string
		magic    int64
		optional bool
	}{
		{"/proc", unix.PROC_SUPER_MAGIC, false},
		{"/sys", unix.SYSFS_MAGIC, false},
		{"/dev", unix.TMPFS_MAGIC, false},
		{"/dev/pts", unix.DEVPTS_SUPER_MAGIC, false},
		{"/dev/dri", unix.TMPFS_MAGIC, true},
		{"/dev/snd", unix.TMPFS_MAGIC, true},
	} {
		if err := selectStructuralObject(role.path, role.magic, selected); err != nil {
			if role.optional && errors.Is(err, unix.ENOENT) {
				continue
			}
			return nil, err
		}
	}
	if err := structuralNullDevice(selected); err != nil {
		return nil, err
	}
	return structuralAncestors(entries, selected)
}

func selectStructuralObject(name string, magic int64, selected map[int]bool) error {
	fd, err := openMountPoint(name)
	if err != nil {
		return fmt.Errorf("open structural role %s: %w", name, err)
	}
	defer func() { _ = unix.Close(fd) }()
	var object unix.Stat_t
	if err := unix.Fstat(fd, &object); err != nil {
		return err
	}
	kind := object.Mode & unix.S_IFMT
	if object.Uid != 0 || object.Mode&0o022 != 0 || (kind != unix.S_IFDIR && kind != unix.S_IFREG) {
		return errors.New("structural role is not a root-controlled file or directory")
	}
	if magic != 0 {
		var filesystem unix.Statfs_t
		if err := unix.Fstatfs(fd, &filesystem); err != nil || filesystem.Type != magic || kind != unix.S_IFDIR {
			return fmt.Errorf("structural kernel role %s has unexpected filesystem facts", name)
		}
	}
	id, err := mountID(fd)
	if err != nil {
		return err
	}
	if id == 0 || id > math.MaxInt {
		return errors.New("structural mount identity exceeds native width")
	}
	selected[int(id)] = true
	return nil
}

func structuralNullDevice(selected map[int]bool) error {
	fd, err := openMountPoint("/dev/null")
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	var object unix.Stat_t
	if err := unix.Fstat(fd, &object); err != nil || object.Uid != 0 || object.Mode&unix.S_IFMT != unix.S_IFCHR || object.Rdev != unix.Mkdev(1, 3) {
		return errors.New("structural null device has unexpected kernel identity")
	}
	id, err := mountID(fd)
	if err != nil {
		return err
	}
	if id == 0 || id > math.MaxInt {
		return errors.New("structural mount identity exceeds native width")
	}
	selected[int(id)] = true
	return nil
}

func structuralAncestors(entries []mountEntry, selected map[int]bool) (map[int]bool, error) {
	byID := make(map[int]mountEntry, len(entries))
	for _, entry := range entries {
		byID[entry.id] = entry
	}
	result := make(map[int]bool)
	for id := range selected {
		entry, exists := byID[id]
		if !exists {
			return nil, errors.New("structural role is absent from selected mount topology")
		}
		for !result[entry.id] {
			result[entry.id] = true
			entry, exists = byID[entry.parent]
			if !exists {
				break
			}
		}
	}
	return result, nil
}
