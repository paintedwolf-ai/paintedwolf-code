//go:build linux

package linuxsandbox

import (
	"errors"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/sys/unix"
)

// mountOp is one mount the filesystem plan asks for, applied in path order so
// a parent's mask exists before anything beneath it.
type mountOp struct {
	path string
	kind opKind
	// clone is a detached copy of the real path, taken before any mask could
	// hide it; only restored paths carry one.
	clone int
}

type opKind int

const (
	opHidden opKind = iota
	opReadOnly
	opRestored
)

// applyFilesystem expresses the plan's masks and restorations as mounts.
func applyFilesystem(fs Filesystem) error {
	var ops []mountOp
	defer func() {
		for _, op := range ops {
			if op.kind == opRestored {
				_ = unix.Close(op.clone)
			}
		}
	}()
	// Keep original mount flags: a grant never makes a host read-only volume
	// writable. /dev is retained only until mountDev rebuilds its curated view.
	seen := map[string]bool{}
	for _, paths := range [][]string{fs.WriteRoots, fs.WriteFiles, {"/dev"}, fs.Restored} {
		for _, p := range paths {
			if seen[p] || !exists(p) {
				continue
			}
			fd, err := cloneTree(p)
			if err != nil {
				return mountFailure("clone "+p, err)
			}
			ops = append(ops, mountOp{path: p, kind: opRestored, clone: fd})
			seen[p] = true
			if p != "/dev" && !metadataWritable(fs, p) {
				if err := setTreeReadOnly(fd); err != nil {
					return mountFailure("seal read restoration "+p, err)
				}
			}
		}
	}
	for _, p := range fs.Hidden {
		if exists(p) {
			ops = append(ops, mountOp{path: p, kind: opHidden})
		}
	}
	for _, p := range fs.ReadOnly {
		if exists(p) {
			ops = append(ops, mountOp{path: p, kind: opReadOnly})
		}
	}
	sort.SliceStable(ops, func(i, j int) bool { return ops[i].path < ops[j].path })
	var hiddenFiles *hiddenFileMask
	for _, op := range ops {
		if op.kind == opHidden && !isDir(op.path) {
			var err error
			hiddenFiles, err = newHiddenFileMask()
			if err != nil {
				return mountFailure("prepare private file mask", err)
			}
			break
		}
	}
	defer func() {
		if hiddenFiles != nil {
			_ = hiddenFiles.close()
		}
	}()
	// Landlock governs content access; the read-only mount view also holds
	// metadata operations for which Landlock defines no access right.
	if err := bindReadOnly("/"); err != nil {
		return mountFailure("seal root mount view", err)
	}
	for _, op := range ops {
		var err error
		switch op.kind {
		case opHidden:
			if isDir(op.path) {
				err = hideDirectory(op.path)
			} else {
				if hiddenFiles == nil {
					return mountFailure(op.path, errors.New("filesystem entry changed kind during setup"))
				}
				err = hiddenFiles.hide(op.path)
			}
		case opReadOnly:
			err = bindReadOnly(op.path)
		case opRestored:
			err = restore(op.clone, op.path)
		}
		if err != nil {
			return mountFailure(op.path, err)
		}
	}
	if hiddenFiles != nil {
		err := hiddenFiles.close()
		hiddenFiles = nil
		if err != nil {
			return mountFailure("close private file-mask descriptor", err)
		}
	}
	// Hidden directories become read-only only after anything restored beneath
	// them has its mountpoint.
	for _, op := range ops {
		if op.kind == opHidden && isDir(op.path) {
			if err := setReadOnlyAt(op.path); err != nil {
				return mountFailure(op.path, err)
			}
		}
	}
	return nil
}

// metadataWritable interprets the same ordered grants and masks for a restored
// read path. Restoring readability alone must not admit metadata writes.
func metadataWritable(fs Filesystem, path string) bool {
	grant, mask := -1, -1
	for _, p := range fs.WriteRoots {
		if underAny(path, []string{p}) && len(p) > grant {
			grant = len(p)
		}
	}
	for _, p := range fs.WriteFiles {
		if path == p && len(p) > grant {
			grant = len(p)
		}
	}
	for _, paths := range [][]string{fs.Hidden, fs.ReadOnly} {
		for _, p := range paths {
			if underAny(path, []string{p}) && len(p) > mask {
				mask = len(p)
			}
		}
	}
	return grant >= 0 && grant >= mask
}

func mountFailure(what string, err error) error {
	return &Unavailability{Code: UnavailableMount, Detail: what + ": " + err.Error()}
}

// hideDirectory replaces a directory with an empty tmpfs. The placeholder is
// writable only until anything restored beneath it has its mountpoint; the
// final read-only pass seals it.
func hideDirectory(path string) error {
	fd, err := newTmpfs("0755", maskTmpfsSize)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	return unix.MoveMount(fd, "", unix.AT_FDCWD, path, unix.MOVE_MOUNT_F_EMPTY_PATH)
}

// bindReadOnly covers a path with a read-only copy of itself.
func bindReadOnly(path string) error {
	clone, err := cloneTree(path)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(clone) }()
	if err := setTreeReadOnly(clone); err != nil {
		return err
	}
	return unix.MoveMount(clone, "", unix.AT_FDCWD, path, unix.MOVE_MOUNT_F_EMPTY_PATH)
}

func setTreeReadOnly(fd int) error {
	return unix.MountSetattr(fd, "", unix.AT_EMPTY_PATH|unix.AT_RECURSIVE, &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY})
}

// restore attaches a clone of the real path over whatever now covers it,
// creating the mountpoint inside a hidden placeholder when needed.
func restore(clone int, path string) error {
	var st unix.Stat_t
	if err := unix.Fstat(clone, &st); err != nil {
		return err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := createPlaceholder(path, 0o444); err != nil {
				return err
			}
		}
	}
	return unix.MoveMount(clone, "", unix.AT_FDCWD, path, unix.MOVE_MOUNT_F_EMPTY_PATH)
}

// createPlaceholder makes an empty file inside a sandbox tmpfs to serve as a
// mountpoint; it is never a durable write.
func createPlaceholder(path string, mode uint32) error {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_WRONLY|unix.O_CLOEXEC, mode)
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

// cloneTree detaches a copy of the mount tree at path.
func cloneTree(path string) (int, error) {
	return unix.OpenTree(unix.AT_FDCWD, path, unix.OPEN_TREE_CLONE|unix.AT_RECURSIVE|unix.O_CLOEXEC)
}

// setReadOnlyAt makes the top mount at path read-only without touching mounts
// beneath it.
func setReadOnlyAt(path string) error {
	return unix.MountSetattr(unix.AT_FDCWD, path, 0, &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY})
}
