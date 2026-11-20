package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/confine/privateguesthost/linuxruntime"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestGuestWorkDirectories(t *testing.T) {
	for _, key := range []string{"HOME", "PATH", "LANG", "LC_ALL", "XDG_CACHE_HOME", "XDG_DATA_HOME", configdir.EnvDev, configdir.EnvConfigDir} {
		t.Setenv(key, "")
	}
	root := t.TempDir()
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
