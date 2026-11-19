package linuxsandbox

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Launch-local storage is sparse and shared by both temp spellings.
const privateTempSize = "64m"

type retainedTempPath struct {
	path  string
	clone int
}

// Clone the finished view so a second spelling of scratch carries its masks.
func mountPrivateTemp(m *PrivateTempMounts) error {
	if m == nil {
		return nil
	}
	retained, err := retainTempPaths(m.Retained)
	if err != nil {
		return err
	}
	defer func() {
		for _, item := range retained {
			_ = unix.Close(item.clone)
		}
	}()
	var backing int
	if m.Scratch == "" {
		backing, err = newTmpfs("0700", privateTempSize)
	} else {
		backing, err = cloneTempPath(m.Scratch)
	}
	if err != nil {
		return fmt.Errorf("prepare temp backing: %w", err)
	}
	defer func() { _ = unix.Close(backing) }()
	for _, alias := range m.Paths {
		fd, err := unix.OpenTree(backing, "", unix.AT_EMPTY_PATH|unix.OPEN_TREE_CLONE|unix.AT_RECURSIVE|unix.O_CLOEXEC)
		if err != nil {
			return err
		}
		err = unix.MoveMount(fd, "", unix.AT_FDCWD, alias, unix.MOVE_MOUNT_F_EMPTY_PATH)
		_ = unix.Close(fd)
		if err != nil {
			return fmt.Errorf("attach %s: %w", alias, err)
		}
	}
	for _, item := range retained {
		for _, alias := range m.Paths {
			if rel, ok := strings.CutPrefix(item.path, alias+"/"); ok {
				if err := attachTempSubtree(alias, rel, item.clone); err != nil {
					return fmt.Errorf("retain %s: %w", item.path, err)
				}
			}
		}
	}
	return nil
}

func retainTempPaths(paths []string) ([]retainedTempPath, error) {
	var kept []retainedTempPath
	for _, p := range paths {
		// A recursive mount already includes nested grants and their floors.
		if len(kept) > 0 && underAny(p, []string{kept[len(kept)-1].path}) {
			continue
		}
		fd, err := cloneTempPath(p)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			for _, item := range kept {
				_ = unix.Close(item.clone)
			}
			return nil, err
		}
		kept = append(kept, retainedTempPath{p, fd})
	}
	return kept, nil
}

func cloneTempPath(path string) (int, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{
		Flags: unix.O_PATH | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return -1, err
	}
	defer func() { _ = unix.Close(fd) }()
	return unix.OpenTree(fd, "", unix.AT_EMPTY_PATH|unix.OPEN_TREE_CLONE|unix.AT_RECURSIVE|unix.O_CLOEXEC)
}

// Create mountpoints only beneath pinned directories, never through a link.
func attachTempSubtree(alias, rel string, clone int) error {
	root, err := unix.Open(alias, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	dir := root
	defer func() { _ = unix.Close(dir) }()
	parts := strings.Split(rel, string(filepath.Separator))
	for i, name := range parts {
		var next int
		if i+1 == len(parts) && !fdIsDir(clone) {
			next, err = tempFileMountpoint(dir, name)
		} else {
			err = unix.Mkdirat(dir, name, 0o700)
			if err == nil || errors.Is(err, unix.EEXIST) {
				next, err = unix.Openat(dir, name, unix.O_DIRECTORY|unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
		}
		if err != nil {
			return err
		}
		_ = unix.Close(dir)
		dir = next
	}
	return unix.MoveMount(clone, "", dir, "", unix.MOVE_MOUNT_F_EMPTY_PATH|unix.MOVE_MOUNT_T_EMPTY_PATH)
}

func tempFileMountpoint(dir int, name string) (int, error) {
	fd, err := unix.Openat(dir, name, unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC, 0o600)
	if errors.Is(err, unix.EEXIST) {
		return unix.Openat2(dir, name, &unix.OpenHow{Flags: unix.O_PATH | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS})
	}
	return fd, err
}
