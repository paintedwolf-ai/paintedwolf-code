//go:build linux

package launchguard

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

type streamGTKFacts struct {
	Initialized bool   `json:"gtk_initialized"`
	Mapped      bool   `json:"chooser_mapped"`
	InputMode   uint32 `json:"stdin_mode"`
	InputInode  uint64 `json:"stdin_inode"`
}

func TestProtectedStandardStreamModuleIngress(t *testing.T) {
	desktop, module := buildStreamGTKFixture(t)
	display := streamFixtureDisplay(t)
	home := t.TempDir()
	settings := filepath.Join(home, ".config", "gtk-3.0")
	testutil.FailErr(t, "create own GTK settings", os.MkdirAll(settings, 0700))
	testutil.FailErr(t, "select exact inherited memfd through GTK", os.WriteFile(filepath.Join(settings, "settings.ini"), []byte("[Settings]\ngtk-modules=/proc/self/fd/0\n"), 0600))
	raw, err := os.ReadFile(module)
	testutil.FailErr(t, "read constructor module", err)
	descriptor, err := unix.MemfdCreate("painted-wolf-stream-module", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	testutil.FailErr(t, "create owned executable memfd", err)
	image := os.NewFile(uintptr(descriptor), "owned-stream-module")
	defer func() { _ = image.Close() }()
	_, err = image.Write(raw)
	testutil.FailErr(t, "write exact constructor bytes", err)
	_, err = unix.FcntlInt(image.Fd(), unix.F_ADD_SEALS, entrySeals)
	testutil.FailErr(t, "seal exact constructor bytes", err)
	var backing unix.Stat_t
	testutil.FailErr(t, "record memfd kernel identity", unix.Fstat(descriptor, &backing))
	t.Logf("module SHA256=%x size=%d device=%d inode=%d mode=%o", sha256.Sum256(raw), len(raw), backing.Dev, backing.Ino, backing.Mode)
	environment := []string{"HOME=" + home, "PATH=/usr/bin:/bin", "DISPLAY=" + display, "LANG=C.UTF-8", "DBUS_SESSION_BUS_ADDRESS=unix:path=" + filepath.Join(home, "absent-session-bus")}
	marker := filepath.Join(home, "stream-module-marker")
	report := filepath.Join(home, "gtk-report.json")
	run := func(program string) streamGTKFacts {
		t.Helper()
		command := exec.Command(program, report)
		command.Env, command.Stdin = environment, image
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("real GTK launch failed: %v\n%s", err, output)
		}
		encoded, err := os.ReadFile(report)
		testutil.FailErr(t, "read actual mapped chooser facts", err)
		var facts streamGTKFacts
		testutil.FailErr(t, "decode actual GTK facts", json.Unmarshal(encoded, &facts))
		if !facts.Initialized || !facts.Mapped {
			t.Fatalf("missing actual GTK success: %+v", facts)
		}
		t.Logf("GTK facts: %s", encoded)
		testutil.FailErr(t, "remove consumed GTK observation", os.Remove(report))
		return facts
	}
	ordinary := run(desktop)
	if ordinary.InputInode != backing.Ino || ordinary.InputMode&unix.S_IFMT != unix.S_IFREG {
		t.Fatalf("ordinary stream differs from selected memfd: %+v", ordinary)
	}
	loaded, err := os.ReadFile(marker)
	testutil.FailErr(t, "ordinary GTK must execute the exact memfd constructor", err)
	if string(loaded) != "loaded\n" {
		t.Fatalf("constructor marker %q", loaded)
	}
	testutil.FailErr(t, "clear positive-control marker", os.Remove(marker))
	fixture := installLaunchFixture(t)
	binaries := append([]string(nil), fixture.binaries...)
	binaries[2] = desktop
	testutil.FailErr(t, "stage actual GTK desktop with exact selected helpers", sudoFixture(t, "upgrade-gtk", fixture.dir, fixtureBinsEnv+"="+strings.Join(binaries, ":")))
	protected := run(fixture.bootstrap)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("caller memfd constructor executed inside protected GTK: %v; facts=%+v", err, protected)
	}
	if protected.InputMode&unix.S_IFMT != unix.S_IFCHR {
		t.Fatalf("unsafe inherited standard input was not replaced: %+v", protected)
	}
}

func buildStreamGTKFixture(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	desktop, module := filepath.Join(directory, "gtk-stream-desktop"), filepath.Join(directory, "module.so")
	flags, err := exec.Command("pkg-config", "--cflags", "--libs", "gtk+-3.0").Output()
	testutil.FailErr(t, "read installed GTK compiler contract", err)
	source := filepath.Join("testdata", "streamdesktop")
	arguments := []string{"-std=c11", "-O2", "-Wall", "-Wextra", "-Werror", filepath.Join(source, "main.c"), "-o", desktop}
	arguments = append(arguments, strings.Fields(string(flags))...)
	if output, err := exec.Command("cc", arguments...).CombinedOutput(); err != nil {
		t.Fatalf("build actual GTK desktop: %v\n%s", err, output)
	}
	if output, err := exec.Command("cc", "-std=c11", "-O2", "-Wall", "-Wextra", "-Werror", "-shared", "-fPIC", filepath.Join(source, "module.c"), "-o", module).CombinedOutput(); err != nil {
		t.Fatalf("build marked GTK module: %v\n%s", err, output)
	}
	testutil.FailErr(t, "make fixture build ancestry traversable", os.Chmod(directory, 0755))
	return desktop, module
}

func streamFixtureDisplay(t *testing.T) string {
	t.Helper()
	return startFixtureXvfb(t)
}
