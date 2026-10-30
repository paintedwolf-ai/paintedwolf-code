//go:build linux

package launchguard

import (
	"errors"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Prune inaccessible administrative mounts through a pinned local parent.
// LOOKUP_MOUNTPOINT in umount2 suppresses terminal weak revalidation; no
// terminal descriptor or metadata operation enters the mounted filesystem.
// Filesystem teardown may still run when the detached tree is released.
func pruneAdministrativeMount(expected mountEntry, entries []mountEntry) error {
	if expected.mountPoint == "/" || (expected.filesystem != "nsfs" && expected.filesystem != "overlay") {
		return errors.New("inaccessible mount has no proven prune-only boundary")
	}
	hidden, err := hiddenMountRoots(entries)
	if err != nil {
		return err
	}
	byAlias := make(map[string]mountEntry, len(entries))
	for _, entry := range entries {
		if hidden[entry.id] {
			continue
		}
		if _, exists := byAlias[entry.mountPoint]; exists {
			return errors.New("ambiguous prune-only mount boundary")
		}
		byAlias[entry.mountPoint] = entry
	}
	if visible, ok := byAlias[expected.mountPoint]; !ok || visible != expected {
		return errors.New("prune-only target is not the visible mount root")
	}
	root, exists := byAlias["/"]
	if !exists || !pruneParentFilesystem(root.filesystem) {
		return errors.New("unproved prune-only root filesystem")
	}
	current, err := unix.Open("/", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(current) }()
	if err := validatePruneParent(current, root); err != nil {
		return err
	}
	parent, leaf := filepath.Split(expected.mountPoint)
	if leaf == "" || leaf == "." || leaf == ".." || strings.Contains(leaf, "/") {
		return errors.New("invalid prune-only terminal component")
	}
	path := ""
	for _, part := range strings.Split(strings.Trim(parent, "/"), "/") {
		if part == "" {
			continue
		}
		path += "/" + part
		boundary, crossing := byAlias[path]
		if !crossing {
			boundary = root
		}
		if !pruneParentFilesystem(boundary.filesystem) {
			return errors.New("unproved filesystem before prune-only target")
		}
		resolve := uint64(unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS)
		if !crossing {
			resolve |= unix.RESOLVE_NO_XDEV
		}
		next, err := unix.Openat2(current, part, &unix.OpenHow{Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: resolve})
		if err != nil {
			return err
		}
		_ = unix.Close(current)
		current = next
		if err := validatePruneParent(current, boundary); err != nil {
			return err
		}
		root = boundary
	}
	// The frozen private topology and sole mount writer bind this visible
	// terminal row to the retained, caller-unmodifiable parent directory.
	return unix.Unmount(mountDescriptorPath(current)+"/"+leaf, unix.MNT_DETACH|unix.UMOUNT_NOFOLLOW)
}

func pruneParentFilesystem(kind string) bool {
	switch kind {
	case "ext4", "xfs", "btrfs", "tmpfs", "ramfs":
		return true
	default:
		return false
	}
}

func validatePruneParent(fd int, expected mountEntry) error {
	if expected.id <= 0 || !pruneParentFilesystem(expected.filesystem) {
		return errors.New("unproved prune-only parent filesystem")
	}
	id, _, err := mountDescriptorIdentity(fd)
	if err != nil || id != uint64(expected.id) {
		return errors.New("prune-only parent mount changed")
	}
	var object unix.Stat_t
	if err := unix.Fstat(fd, &object); err != nil {
		return err
	}
	if object.Uid != 0 || object.Mode&unix.S_IFMT != unix.S_IFDIR || object.Mode&0o022 != 0 {
		return errors.New("prune-only parent is not root-controlled")
	}
	return nil
}
