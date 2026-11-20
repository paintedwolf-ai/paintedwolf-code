package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"golang.org/x/sys/unix"
)

const (
	workUID   = 65534
	workGID   = 65534
	workBase  = "/home/service"
	jobGroups = "/sys/fs/cgroup/pw-jobs"
)

type configuration struct{ scratch, groups, helper string }

func prepareVM() (configuration, error) {
	for _, mount := range []struct {
		path, kind, options string
		flags               uintptr
	}{
		{"/proc", "proc", "", unix.MS_NOSUID | unix.MS_NOEXEC | unix.MS_NODEV},
		{"/sys", "sysfs", "", unix.MS_NOSUID | unix.MS_NOEXEC | unix.MS_NODEV},
		{"/dev", "devtmpfs", "mode=0755", unix.MS_NOSUID},
		{"/dev/shm", "tmpfs", "size=32M,mode=1777", unix.MS_NOSUID | unix.MS_NODEV},
		{"/tmp", "tmpfs", "size=16M,mode=1777", unix.MS_NOSUID | unix.MS_NODEV},
		{workBase, "tmpfs", "size=256M,mode=0700,uid=65534,gid=65534", unix.MS_NOSUID | unix.MS_NODEV},
		{"/sys/fs/cgroup", "cgroup2", "", unix.MS_NOSUID | unix.MS_NOEXEC | unix.MS_NODEV},
	} {
		if err := os.MkdirAll(mount.path, 0755); err != nil {
			return configuration{}, err
		}
		if err := unix.Mount(mount.kind, mount.path, mount.kind, mount.flags, mount.options); err != nil && !errors.Is(err, unix.EBUSY) {
			return configuration{}, fmt.Errorf("mount %s: %w", mount.path, err)
		}
	}
	if err := loadBootModules(); err != nil {
		return configuration{}, err
	}
	scratch, err := prepareWork(workUID)
	if err != nil {
		return configuration{}, err
	}
	if err := os.WriteFile("/sys/fs/cgroup/cgroup.subtree_control", []byte("+cpu +memory +pids"), 0600); err != nil {
		return configuration{}, err
	}
	if err := os.Mkdir(jobGroups, 0700); err != nil {
		return configuration{}, err
	}
	for name, value := range map[string]string{"pids.max": "256", "memory.max": "402653184", "cpu.max": "100000 100000", "cgroup.subtree_control": "+cpu +memory +pids"} {
		if err := os.WriteFile(filepath.Join(jobGroups, name), []byte(value), 0600); err != nil {
			return configuration{}, err
		}
	}
	return configuration{scratch: scratch, groups: jobGroups, helper: "/init"}, nil
}

func loadBootModules() error {
	const base = "/lib/pw-boot-modules"
	entries, err := os.ReadDir(base)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".ko") {
			return fmt.Errorf("invalid guest module %q", entry.Name())
		}
		file, err := os.Open(filepath.Join(base, entry.Name()))
		if err != nil {
			return err
		}
		err = unix.FinitModule(int(file.Fd()), "", 0)
		_ = file.Close()
		if err != nil && !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("load %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func prepareWork(uid int) (string, error) {
	for key, value := range map[string]string{"HOME": workBase, "PATH": "/usr/local/bin:/usr/bin:/bin", "LANG": "C", "LC_ALL": "C"} {
		if err := os.Setenv(key, value); err != nil {
			return "", err
		}
	}
	state, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	scratch := enginepaths.SessionScratchUnder(state, "session")
	for _, p := range []string{filepath.Dir(state), state, filepath.Dir(scratch), scratch, filepath.Join(scratch, "home"), filepath.Join(scratch, "tmp"), filepath.Join(scratch, "cache"), filepath.Join(scratch, "data")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			return "", err
		}
		if err := os.Chown(p, uid, uid); err != nil {
			return "", err
		}
	}
	if target, err := os.Readlink("/work"); err != nil || filepath.Clean(filepath.Join("/", target)) != scratch {
		return "", errors.New("invalid guest work path")
	}
	for key, value := range map[string]string{"XDG_CACHE_HOME": filepath.Join(scratch, "cache"), "XDG_DATA_HOME": filepath.Join(scratch, "data")} {
		if err := os.Setenv(key, value); err != nil {
			return "", err
		}
	}
	return scratch, nil
}
