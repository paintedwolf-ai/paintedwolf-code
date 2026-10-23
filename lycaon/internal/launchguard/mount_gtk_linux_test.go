//go:build linux

package launchguard

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

type gtkVolumeFacts struct {
	Initialized bool `json:"gtk_initialized"`
	ChooserRead bool `json:"chooser_read"`
	EngineRead  bool `json:"engine_read"`
}

// The ordinary lane must execute the exact constructor-marked module through
// GTK settings. The protected lane must initialize a real mapped chooser and
// read the same volume in desktop and engine views without executing it.
func TestMountGTKSettingsModuleIsRefusedWhileChooserReadsFUSE(t *testing.T) {
	desktop, module := buildGTKVolumeFixture(t)
	display := startFixtureXvfb(t)
	home, volume := t.TempDir(), t.TempDir()
	raw, err := os.ReadFile(module)
	testutil.FailErr(t, "read exact constructor-marked module", err)
	t.Logf("GTK module SHA256=%x size=%d", sha256.Sum256(raw), len(raw))
	testutil.FailErr(t, "prepare readable unmounted volume backing", os.WriteFile(filepath.Join(volume, "payload"), []byte("caller-owned-volume\n"), 0600))
	stop := startSourceFUSE(t, volume, raw)
	settings := filepath.Join(home, ".config", "gtk-3.0")
	testutil.FailErr(t, "prepare own GTK settings", os.MkdirAll(settings, 0700))
	testutil.FailErr(t, "select the absolute FUSE module through GTK settings", os.WriteFile(filepath.Join(settings, "settings.ini"), []byte("[Settings]\ngtk-modules="+filepath.Join(volume, "module.so")+"\n"), 0600))
	environment := []string{"HOME=" + home, "PATH=/usr/bin:/bin", "DISPLAY=" + display, "GDK_BACKEND=x11", "LANG=C.UTF-8", "DBUS_SESSION_BUS_ADDRESS=unix:path=" + filepath.Join(home, "absent-session-bus")}
	payload := filepath.Join(volume, "payload")
	ordinary := exec.Command(desktop, "--ordinary-read", payload)
	ordinary.Env = environment
	output, err := ordinary.CombinedOutput()
	if err != nil {
		t.Fatalf("ordinary GTK positive control: %v\n%s", err, output)
	}
	marker := filepath.Join(home, "gtk-module-marker")
	loaded, err := os.ReadFile(marker)
	testutil.FailErr(t, "ordinary GTK settings actually executed the exact module constructor", err)
	if string(loaded) != "loaded\n" {
		t.Fatalf("unexpected constructor marker %q", loaded)
	}
	testutil.FailErr(t, "clear only the positive-control marker", os.Remove(marker))
	fixture := installLaunchFixture(t)
	binaries := append([]string(nil), fixture.binaries...)
	binaries[2] = desktop
	testutil.FailErr(t, "stage GTK desktop with current selected launch helpers and exact manifest", sudoFixture(t, "upgrade", fixture.dir, fixtureBinsEnv+"="+strings.Join(binaries, ":")))
	stop()
	cycleGTKVolumeStartup(t, fixture.bootstrap, environment, volume, raw, marker)
	startSourceFUSE(t, volume, raw)
	for attempt := 0; attempt < 3; attempt++ {
		protected := exec.Command(fixture.bootstrap, "--protected-read", payload)
		protected.Env = environment
		output, err := protected.CombinedOutput()
		if err != nil {
			t.Fatalf("protected GTK successful-GUI lane %d: %v\n%s", attempt, err, output)
		}
		var facts gtkVolumeFacts
		found := false
		for _, line := range strings.Split(string(output), "\n") {
			if record, ok := strings.CutPrefix(line, "PWGTK01 "); ok {
				if found {
					t.Fatal("duplicate GTK fixture report")
				}
				found = true
				testutil.FailErr(t, "decode exact GTK fixture report", json.Unmarshal([]byte(record), &facts))
			}
		}
		if !found {
			t.Fatalf("GTK fixture report missing: %s", output)
		}
		if !facts.Initialized || !facts.ChooserRead || !facts.EngineRead {
			t.Fatalf("protected GTK/chooser/engine positive control absent: %+v\n%s", facts, output)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("FUSE GTK module executed in protected view: %v", err)
		}
	}
}

// Failed concurrent starts supply no positive GUI evidence. At least one cycle
// must finish the actual chooser/read contract, in addition to the stable lane.
func cycleGTKVolumeStartup(t *testing.T, bootstrap string, environment []string, volume string, module []byte, marker string) {
	t.Helper()
	successes, mounts := 0, 0
	began := time.Now()
	for attempt := 0; attempt < 12; attempt++ {
		stop := startSourceFUSE(t, volume, module)
		mounts++
		var output bytes.Buffer
		command := exec.Command(bootstrap, "--protected-read", filepath.Join(volume, "payload"))
		command.Env = environment
		command.Stdout, command.Stderr = &output, &output
		testutil.FailErr(t, "start protected GTK during owned FUSE mount cycles", command.Start())
		done := make(chan struct{})
		var waitErr error
		go func() { waitErr = command.Wait(); close(done) }()
		t.Cleanup(func() { _ = command.Process.Kill(); <-done })
		for cycle := 0; cycle < 3; cycle++ {
			stop()
			stop = startSourceFUSE(t, volume, module)
			mounts++
			time.Sleep(5 * time.Millisecond)
		}
		<-done
		err := waitErr
		stop()
		if err == nil {
			if !strings.Contains(output.String(), `PWGTK01 {"gtk_initialized":true,"chooser_read":true,"engine_read":true}`) {
				t.Fatalf("successful concurrent launch omitted positive GTK/read report: %s", output.String())
			}
			successes++
		} else {
			t.Logf("concurrent launch %d supplied no GUI success evidence: %v\n%s", attempt, err, output.String())
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("FUSE settings module constructor executed during concurrent launch: %v", err)
		}
	}
	t.Logf("FUSE startup cycles: %d mounts, %d successful protected GUI/read launches of 12, elapsed %s", mounts, successes, time.Since(began))
	if successes == 0 {
		t.Fatal("mount cycles produced no successful protected GTK chooser/read launch")
	}
}

func buildGTKVolumeFixture(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	desktop, module := filepath.Join(directory, "gtk-volume-desktop"), filepath.Join(directory, "module.so")
	flags, err := exec.Command("pkg-config", "--cflags", "--libs", "gtk+-3.0").Output()
	testutil.FailErr(t, "read installed GTK build contract", err)
	source := filepath.Join("testdata", "gtkdesktop")
	arguments := []string{"-std=c11", "-O2", "-Wall", "-Wextra", "-Werror", filepath.Join(source, "main.c"), "-o", desktop}
	arguments = append(arguments, strings.Fields(string(flags))...)
	if output, err := exec.Command("cc", arguments...).CombinedOutput(); err != nil {
		t.Fatalf("build actual GTK chooser fixture: %v\n%s", err, output)
	}
	if output, err := exec.Command("cc", "-std=c11", "-O2", "-Wall", "-Wextra", "-Werror", "-shared", "-fPIC", filepath.Join(source, "module.c"), "-o", module).CombinedOutput(); err != nil {
		t.Fatalf("build marked native GTK module: %v\n%s", err, output)
	}
	testutil.FailErr(t, "make caller build ancestry traversable for fixture installation", os.Chmod(directory, 0755))
	return desktop, module
}

func startFixtureXvfb(t *testing.T) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	testutil.FailErr(t, "create own X display readiness", err)
	defer reader.Close()
	defer writer.Close()
	log, err := os.Create(filepath.Join(t.TempDir(), "xvfb.log"))
	testutil.FailErr(t, "retain own X display diagnostics", err)
	t.Cleanup(func() { _ = log.Close() })
	command := exec.Command("Xvfb", "-displayfd", "3", "-screen", "0", "800x600x24", "-nolisten", "tcp")
	command.ExtraFiles = []*os.File{writer}
	command.Stdout = log
	command.Stderr = log
	testutil.FailErr(t, "start own isolated X display", command.Start())
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	_ = writer.Close()
	testutil.FailErr(t, "bound own X display startup", reader.SetReadDeadline(time.Now().Add(5*time.Second)))
	text, err := bufio.NewReader(reader).ReadString('\n')
	testutil.FailErr(t, "receive own X display number", err)
	number, err := strconv.ParseUint(strings.TrimSpace(text), 10, 16)
	testutil.FailErr(t, "decode own X display number", err)
	return fmt.Sprintf(":%d", number)
}
