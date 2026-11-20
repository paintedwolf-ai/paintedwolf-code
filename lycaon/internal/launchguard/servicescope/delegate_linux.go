// Package servicescope bounds private service processes under the launching user's cgroup.
package servicescope

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	hierarchy   = "/sys/fs/cgroup"
	class       = "paintedwolf-services"
	controllers = "+memory +pids"
)

func current() (string, error) {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok && filepath.IsAbs(path) && filepath.Clean(path) == path {
			return filepath.Join(hierarchy, path), nil
		}
	}
	return "", errors.New("unified cgroup unavailable")
}

func identity(uid int) string { return "uid-" + strconv.Itoa(uid) }

// DelegateForBroker provisions descendants of the broker's original resource domain.
func DelegateForBroker(uid int) error {
	if os.Geteuid() != 0 || uid <= 0 {
		return unix.EPERM
	}
	currentGroup, err := current()
	if err != nil {
		return err
	}
	root, err := unix.Openat2(unix.AT_FDCWD, hierarchy, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return err
	}
	defer unix.Close(root)
	var fs unix.Statfs_t
	if err := unix.Fstatfs(root, &fs); err != nil {
		return err
	}
	if fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return errors.New("private service hierarchy is not cgroup v2")
	}
	relative, err := filepath.Rel(hierarchy, currentGroup)
	if err != nil {
		return err
	}
	currentDir := root
	if relative != "." {
		for _, name := range strings.Split(relative, "/") {
			fd, err := openDirectory(currentDir, name, false, 0)
			if err != nil {
				return err
			}
			defer unix.Close(fd)
			currentDir = fd
		}
	}
	if err := writeControl(currentDir, "cgroup.subtree_control", controllers); err != nil {
		return err
	}
	for _, name := range []string{class, identity(uid)} {
		fd, err := openDirectory(currentDir, name, true, 0)
		if err != nil {
			return err
		}
		defer unix.Close(fd)
		currentDir = fd
		if err := writeControl(fd, "cgroup.subtree_control", controllers); err != nil {
			return err
		}
	}
	manager, err := openDirectory(currentDir, "ordinary", true, 0)
	if err != nil {
		return err
	}
	defer unix.Close(manager)
	pool, err := openDirectory(currentDir, "sessions", true, uint32(uid))
	if err != nil {
		return err
	}
	defer unix.Close(pool)
	for file, value := range limits(4) {
		if err := writeControl(pool, file, value); err != nil {
			return err
		}
	}
	if err := writeControl(pool, "cgroup.subtree_control", controllers); err != nil {
		return err
	}
	for _, name := range []string{"cgroup.procs", "cgroup.threads", "cgroup.subtree_control"} {
		if err := delegateControl(pool, name, uid); err != nil {
			return err
		}
	}
	if err := unix.Fchown(pool, uid, -1); err != nil {
		return err
	}
	if err := delegateControl(currentDir, "cgroup.procs", uid); err != nil {
		return err
	}
	return writeControl(manager, "cgroup.procs", "0")
}

func openDirectory(parent int, name string, create bool, owner uint32) (int, error) {
	if create {
		if err := unix.Mkdirat(parent, name, 0755); err != nil && !errors.Is(err, unix.EEXIST) {
			return -1, err
		}
	}
	fd, err := openObject(parent, name, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || (stat.Uid != 0 && stat.Uid != owner) || stat.Mode&0022 != 0 {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("untrusted service cgroup %q", name)
	}
	return fd, nil
}

// Privileged writes stay on the captured cgroup mount, including beneath delegated directories.
func openObject(parent int, name string, flags uint64) (int, error) {
	return unix.Openat2(parent, name, &unix.OpenHow{Flags: flags | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_XDEV | unix.RESOLVE_NO_SYMLINKS})
}

func writeControl(parent int, name, value string) error {
	fd, err := openObject(parent, name, unix.O_WRONLY)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	n, err := unix.Write(fd, []byte(value))
	if err == nil && n != len(value) {
		return errors.New("incomplete cgroup control write")
	}
	return err
}

func delegateControl(parent int, name string, uid int) error {
	fd, err := openObject(parent, name, unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return unix.Fchown(fd, uid, -1)
}

func limits(sessions int) map[string]string {
	return map[string]string{"memory.max": strconv.Itoa(sessions * (512 << 20)), "pids.max": strconv.Itoa(sessions * 256)}
}

func setLimits(path string, sessions int) error {
	for name, value := range limits(sessions) {
		if err := os.WriteFile(filepath.Join(path, name), []byte(value), 0600); err != nil {
			return err
		}
	}
	return nil
}
