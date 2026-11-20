package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/confine/privateguesthost/linuxruntime"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestGuestWorkDirectories(t *testing.T) {
	for _, key := range []string{"HOME", "PATH", "LANG", "LC_ALL", "XDG_CACHE_HOME", "XDG_DATA_HOME", configdir.EnvDev} {
		t.Setenv(key, "")
	}
	root := t.TempDir()
	t.Setenv(configdir.EnvConfigDir, filepath.Join(root, "host-override"))
	home, link := filepath.Join(root, "home"), filepath.Join(root, "work")
	want := enginepaths.SessionScratchUnder(configdir.ChannelConfigRoot(home), "session")
	if _, err := prepareWork(home, link, os.Getuid()); err == nil {
		t.Fatal("missing work link accepted")
	}
	rel, err := filepath.Rel(root, want)
	testutil.FailErr(t, "locate relative workspace", err)
	testutil.FailErr(t, "link guest workspace", os.Symlink(rel, link))
	got, err := prepareWork(home, link, os.Getuid())
	testutil.FailErr(t, "prepare guest workspace", err)
	if got != want || os.Getenv("XDG_CACHE_HOME") != filepath.Join(want, "cache") || os.Getenv("XDG_DATA_HOME") != filepath.Join(want, "data") {
		t.Fatalf("invalid guest workspace %q", got)
	}
	for _, name := range []string{"home", "tmp", "data", "cache"} {
		info, err := os.Stat(filepath.Join(link, name))
		testutil.FailErr(t, "inspect guest work directory", err)
		if !info.IsDir() || info.Mode().Perm() != 0700 {
			t.Fatalf("%s mode = %s", name, info.Mode())
		}
	}
}

func TestGuestModulesAndControllerPort(t *testing.T) {
	root := t.TempDir()
	modules := filepath.Join(root, "modules")
	testutil.FailErr(t, "boot with built-in drivers", loadBootModules(modules))
	testutil.FailErr(t, "create module directory", os.Mkdir(modules, 0755))
	testutil.FailErr(t, "write unexpected module entry", os.WriteFile(filepath.Join(modules, "unknown"), nil, 0600))
	if err := loadBootModules(modules); err == nil {
		t.Fatal("unexpected module entry accepted")
	}
	ports, devices := filepath.Join(root, "ports"), filepath.Join(root, "devices")
	for _, path := range []string{filepath.Join(ports, "vport1p1"), devices} {
		testutil.FailErr(t, "create port fixture", os.MkdirAll(path, 0755))
	}
	name := filepath.Join(ports, "vport1p1", "name")
	if _, err := privateConsole(ports, devices, time.Now()); err == nil {
		t.Fatal("unidentified port accepted")
	}
	testutil.FailErr(t, "name unrelated port", os.WriteFile(name, []byte("unrelated"), 0600))
	if _, err := privateConsole(ports, devices, time.Now()); err == nil {
		t.Fatal("unrelated port accepted")
	}
	testutil.FailErr(t, "name controller port", os.WriteFile(name, []byte(linuxruntime.ConsoleName+"\n"), 0600))
	testutil.FailErr(t, "create controller device", os.WriteFile(filepath.Join(devices, "vport1p1"), nil, 0600))
	controller, err := privateConsole(ports, devices, time.Now())
	testutil.FailErr(t, "open controller device", err)
	testutil.FailErr(t, "close controller", controller.Close())
}

func TestGuestBootMounts(t *testing.T) {
	root := t.TempDir()
	type mounted struct {
		kind, path, options string
		flags               uintptr
	}
	var mounts []mounted
	attach := func(source, path, kind string, flags uintptr, options string) error {
		info, err := os.Stat(path)
		testutil.FailErr(t, "inspect mount point", err)
		if !info.IsDir() || source != kind {
			t.Fatalf("mount point %s has type %s, source %s, kind %s", path, info.Mode(), source, kind)
		}
		rel, err := filepath.Rel(root, path)
		testutil.FailErr(t, "locate VM filesystem", err)
		mounts = append(mounts, mounted{kind, rel, options, flags})
		if kind == "devtmpfs" {
			return unix.EBUSY
		}
		return nil
	}
	testutil.FailErr(t, "mount guest filesystems", mountVM(root, attach))
	want := []mounted{
		{"proc", "proc", "", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC},
		{"sysfs", "sys", "", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC},
		{"devtmpfs", "dev", "mode=0755", unix.MS_NOSUID},
		{"tmpfs", "dev/shm", "size=32M,mode=1777", unix.MS_NOSUID | unix.MS_NODEV},
		{"tmpfs", "tmp", "size=16M,mode=1777", unix.MS_NOSUID | unix.MS_NODEV},
		{"tmpfs", "home/service", "size=256M,mode=0700,uid=65534,gid=65534", unix.MS_NOSUID | unix.MS_NODEV},
		{"cgroup2", "sys/fs/cgroup", "", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC},
	}
	if !slices.Equal(mounts, want) {
		t.Fatalf("guest mounts = %+v", mounts)
	}
	if err := mountVM(root, func(string, string, string, uintptr, string) error { return unix.EPERM }); !errors.Is(err, unix.EPERM) {
		t.Fatalf("mount rejection = %v", err)
	}
}

func TestGuestResourceDomain(t *testing.T) {
	root := t.TempDir()
	group, err := prepareJobGroups(root)
	testutil.FailErr(t, "prepare job resources", err)
	if group != filepath.Join(root, "pw-jobs") {
		t.Fatalf("unexpected group %q", group)
	}
	for name, want := range map[string]string{"cgroup.subtree_control": "+cpu +memory +pids", "pw-jobs/cgroup.subtree_control": "+cpu +memory +pids", "pw-jobs/cpu.max": "100000 100000", "pw-jobs/memory.max": "402653184", "pw-jobs/pids.max": "256"} {
		got, err := os.ReadFile(filepath.Join(root, name))
		testutil.FailErr(t, "inspect job resource", err)
		if string(got) != want {
			t.Fatalf("%s=%q, want %q", name, got, want)
		}
	}
	if _, err := prepareJobGroups(root); !errors.Is(err, os.ErrExist) {
		t.Fatalf("reused guest group = %v", err)
	}
	if _, err := prepareJobGroups(filepath.Join(root, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing domain = %v", err)
	}
}
