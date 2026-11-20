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
	var fs unix.Statfs_t
	if err := unix.Statfs(hierarchy, &fs); err != nil {
		return err
	}
	if fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return errors.New("private service hierarchy is not cgroup v2")
	}
	path := hierarchy
	if err := controlledDirectory(path); err != nil {
		return err
	}
	relative, err := filepath.Rel(hierarchy, currentGroup)
	if err != nil {
		return err
	}
	if relative != "." {
		for _, entry := range strings.Split(relative, "/") {
			path = filepath.Join(path, entry)
			if err := controlledDirectory(path); err != nil {
				return err
			}
		}
	}
	if err := enable(path); err != nil {
		return err
	}
	for _, name := range []string{class, identity(uid)} {
		path = filepath.Join(path, name)
		if err := makeControlled(path); err != nil {
			return err
		}
		if err := enable(path); err != nil {
			return err
		}
	}
	manager := filepath.Join(path, "ordinary")
	if err := makeControlled(manager); err != nil {
		return err
	}
	pool := filepath.Join(path, "sessions")
	// The root-owned parent fixes the pool's name and aggregate ceilings.
	if err := os.Mkdir(pool, 0755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	stat, err := os.Lstat(pool)
	if err != nil {
		return err
	}
	if !stat.IsDir() {
		return unix.ENOTDIR
	}
	if err := setLimits(pool, 4); err != nil {
		return err
	}
	if err := enable(pool); err != nil {
		return err
	}
	for _, name := range []string{"cgroup.procs", "cgroup.threads", "cgroup.subtree_control", ""} {
		if err := os.Chown(filepath.Join(pool, name), uid, -1); err != nil {
			return err
		}
	}
	if err := os.Chown(filepath.Join(path, "cgroup.procs"), uid, -1); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(manager, "cgroup.procs"), []byte("0"), 0600)
}

func controlledDirectory(path string) error {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != 0 || st.Mode&0022 != 0 {
		return fmt.Errorf("untrusted cgroup %q", path)
	}
	return nil
}

func makeControlled(path string) error {
	if err := os.Mkdir(path, 0755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return controlledDirectory(path)
}

func enable(path string) error {
	return os.WriteFile(filepath.Join(path, "cgroup.subtree_control"), []byte(controllers), 0600)
}

func setLimits(path string, sessions int) error {
	for name, value := range map[string]string{"memory.max": strconv.Itoa(sessions * (512 << 20)), "pids.max": strconv.Itoa(sessions * 256)} {
		if err := os.WriteFile(filepath.Join(path, name), []byte(value), 0600); err != nil {
			return err
		}
	}
	return nil
}
