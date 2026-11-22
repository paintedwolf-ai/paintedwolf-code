//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Structural selection is an exact opened-role set, closed over mount parents.
// A filesystem type, shared superblock or pathname prefix never selects peers
// or descendants. Every other inherited mount is removed before U_view exists.
// These are fixed kernel interfaces, not filesystem classification rules.
// Unknown child masks are restored as safe initial leases before readiness.
var structuralKernelRoles = []struct {
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
}

const structuralNullPath = "/dev/null"

// structuralObjectPaths are the opened roles that select structural mounts.
// The held generation is already bound at its logical root. Its store path
// can be hidden beneath that bind when the store lives inside it.
func structuralObjectPaths(policy GenerationPolicy, interpreter string) []string {
	return append(moduleRoots(policy, interpreter), policy.LogicalInstallRoot, "/")
}

// structuralResolvedPaths is every path whose resolution decides the selection.
func structuralResolvedPaths(policy GenerationPolicy, image *os.File) ([]string, error) {
	interpreter, err := programInterpreter(image)
	if err != nil {
		return nil, err
	}
	paths := structuralObjectPaths(policy, interpreter)
	for _, role := range structuralKernelRoles {
		paths = append(paths, role.path)
	}
	return append(paths, structuralNullPath), nil
}

func structuralMounts(policy GenerationPolicy, image *os.File, entries []mountEntry) (map[int]bool, error) {
	interpreter, err := programInterpreter(image)
	if err != nil {
		return nil, err
	}
	selected := make(map[int]bool)
	for _, name := range structuralObjectPaths(policy, interpreter) {
		if err := selectStructuralObject(name, 0, selected); err != nil {
			return nil, err
		}
	}
	for _, role := range structuralKernelRoles {
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
	fd, err := openMountPoint(structuralNullPath)
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

// sameSelectionTopology compares only what a structural selection depends on:
// every selected mount, and any mount at or above a path the selection
// resolved, which could cover or redirect it. Mounts elsewhere still reach a
// launch through receive-only propagation and leave the selection unchanged.
func sameSelectionTopology(before, after []mountEntry, selected map[int]bool, paths []string) bool {
	relevant := func(entry mountEntry) bool {
		if selected[entry.id] {
			return true
		}
		for _, path := range paths {
			if mountPointAtOrAbove(entry.mountPoint, path) {
				return true
			}
		}
		return false
	}
	remaining := make(map[int]mountEntry)
	for _, entry := range before {
		if relevant(entry) {
			remaining[entry.id] = entry
		}
	}
	for _, entry := range after {
		if !relevant(entry) {
			if _, was := remaining[entry.id]; was {
				return false
			}
			continue
		}
		expected, exists := remaining[entry.id]
		if !exists || expected != entry {
			return false
		}
		delete(remaining, entry.id)
	}
	return len(remaining) == 0
}

func mountPointAtOrAbove(mountPoint, path string) bool {
	return mountPoint == path || mountPoint == "/" || strings.HasPrefix(path, mountPoint+"/")
}
