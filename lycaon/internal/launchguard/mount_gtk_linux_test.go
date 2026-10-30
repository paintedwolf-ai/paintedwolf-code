//go:build linux

package launchguard

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	assertGTKVolumeHold(t, fixture.bootstrap, environment, payload)
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
	retainGTKVolumeComponent(t, desktop, module)
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

type gtkHeldFile struct {
	Held   bool   `json:"held"`
	PID    int    `json:"pid"`
	FD     int    `json:"fd"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

func assertGTKVolumeHold(t *testing.T, bootstrap string, environment []string, payload string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, bootstrap, "--protected-hold", payload)
	command.Env = environment
	input, err := command.StdinPipe()
	testutil.FailErr(t, "prepare held-file control", err)
	output, reportWriter, err := os.Pipe()
	testutil.FailErr(t, "prepare owned held-file report pipe", err)
	defer output.Close()
	defer reportWriter.Close()
	command.Stdout = reportWriter
	diagnostic, err := os.Create(filepath.Join(t.TempDir(), "gtk-held-stderr.log"))
	testutil.FailErr(t, "retain held-file diagnostics", err)
	defer diagnostic.Close()
	command.Stderr = diagnostic
	diagnostics := func() string { raw, _ := os.ReadFile(diagnostic.Name()); return string(raw) }
	testutil.FailErr(t, "start selected-generation GTK held-file lane", command.Start())
	_ = reportWriter.Close()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	waited := false
	defer func() {
		_ = input.Close()
		if !waited {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				cancel()
				<-done
			}
		}
	}()
	testutil.FailErr(t, "bound held-file report reads independently of child exit", output.SetReadDeadline(time.Now().Add(10*time.Second)))
	reader := bufio.NewReader(output)
	read := func(prefix string, target any) {
		t.Helper()
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read GTK held-file report: %v stderr=%s", err, diagnostics())
		}
		raw, ok := strings.CutPrefix(line, prefix)
		if !ok {
			t.Fatalf("unexpected GTK held-file protocol %q", line)
		}
		testutil.FailErr(t, "decode GTK held-file record", json.Unmarshal([]byte(raw), target))
	}
	var view gtkVolumeFacts
	read("PWGTK01 ", &view)
	if !view.Initialized || !view.ChooserRead || !view.EngineRead {
		t.Fatalf("held-file lane omitted actual GUI/read proof: %+v", view)
	}
	var held gtkHeldFile
	read("PWGTKHOLD01 ", &held)
	if !held.Held || held.PID <= 1 || held.FD < 0 || held.Inode == 0 {
		t.Fatalf("GTK did not retain actual regular-file descriptor: %+v", held)
	}
	_, err = input.Write([]byte{'s'})
	testutil.FailErr(t, "query still-open descriptor", err)
	var again gtkHeldFile
	read("PWGTKHOLD01 ", &again)
	if again != held {
		t.Fatalf("held descriptor changed before release: %+v -> %+v", held, again)
	}
	_, err = input.Write([]byte{'c'})
	testutil.FailErr(t, "release only the selected held descriptor", err)
	var closed gtkHeldFile
	read("PWGTKHOLD01 ", &closed)
	if closed.Held || closed.FD != -1 || closed.PID != held.PID || closed.Inode != held.Inode || closed.Device != held.Device {
		t.Fatalf("GTK held descriptor was not explicitly released: %+v", closed)
	}
	_, err = input.Write([]byte{'s'})
	testutil.FailErr(t, "query explicit closed state while GUI remains alive", err)
	var stillClosed gtkHeldFile
	read("PWGTKHOLD01 ", &stillClosed)
	if stillClosed != closed {
		t.Fatalf("closed-file component state changed: %+v -> %+v", closed, stillClosed)
	}
	_, err = input.Write([]byte{'q'})
	testutil.FailErr(t, "end held-file component", err)
	err = <-done
	waited = true
	if err != nil {
		t.Fatalf("GTK held-file lane did not settle: %v stderr=%s", err, diagnostics())
	}
}

// Preserve compiled component applications for later owned-device manual probes;
// they are not managed test binaries and carry no installed-qualification claim.
func retainGTKVolumeComponent(t *testing.T, desktop, module string) {
	t.Helper()
	root := os.Getenv("PW_TEST_ARTIFACT_ROOT")
	if root == "" {
		root = os.Getenv("PW_ARTIFACT_ROOT")
	}
	if root == "" {
		t.Fatal("managed artifact root unavailable for GTK component retention")
	}
	root = filepath.Join(root, "manual-components")
	testutil.FailErr(t, "prepare component artifact owner", os.MkdirAll(root, 0700))
	directory := filepath.Join(root, "gtk-volume-"+rand.Text())
	testutil.FailErr(t, "create distinct retained component", os.Mkdir(directory, 0700))
	files := map[string]string{}
	for name, source := range map[string]string{"gtk-volume-desktop": desktop, "module.so": module} {
		raw, err := os.ReadFile(source)
		testutil.FailErr(t, "read compiled component", err)
		testutil.FailErr(t, "retain compiled component", os.WriteFile(filepath.Join(directory, name), raw, 0555))
		files[name] = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	raw, err := json.MarshalIndent(map[string]any{"format_version": 1, "architecture": runtime.GOARCH, "files_sha256": files, "scope": "compiled component preparation; not installed qualification"}, "", "  ")
	testutil.FailErr(t, "encode component provenance", err)
	testutil.FailErr(t, "retain component provenance", os.WriteFile(filepath.Join(directory, "component.json"), append(raw, '\n'), 0600))
	t.Logf("retained GTK volume component: %s", directory)
}

func TestMountGTKChooserReadsVolumeAttachedAfterReadiness(t *testing.T) {
	desktop, module := buildGTKVolumeFixture(t)
	display := startFixtureXvfb(t)
	home, volume := t.TempDir(), t.TempDir()
	payload := filepath.Join(volume, "payload")
	if _, err := os.Stat(payload); !os.IsNotExist(err) {
		t.Fatalf("late-volume payload exists before attachment: %v", err)
	}
	raw, err := os.ReadFile(module)
	testutil.FailErr(t, "read late-volume module", err)
	fixture := installLaunchFixture(t)
	binaries := append([]string(nil), fixture.binaries...)
	binaries[2] = desktop
	testutil.FailErr(t, "stage late-volume GTK desktop", sudoFixture(t, "upgrade", fixture.dir, fixtureBinsEnv+"="+strings.Join(binaries, ":")))
	testutil.FailErr(t, "exercise native nested generation store", sudoFixture(t, "nested-generation", fixture.dir))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, fixture.bootstrap, "--protected-watch", payload)
	command.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "DISPLAY=" + display, "GDK_BACKEND=x11", "LANG=C.UTF-8", "DBUS_SESSION_BUS_ADDRESS=unix:path=" + filepath.Join(home, "absent-session-bus")}
	input, err := command.StdinPipe()
	testutil.FailErr(t, "create late-volume controls", err)
	defer input.Close()
	output, writer, err := os.Pipe()
	testutil.FailErr(t, "create late-volume reports", err)
	defer output.Close()
	defer writer.Close()
	diagnostic, err := os.Create(filepath.Join(t.TempDir(), "gtk-late.log"))
	testutil.FailErr(t, "retain late-volume diagnostics", err)
	defer diagnostic.Close()
	command.Stdout, command.Stderr = writer, diagnostic
	testutil.FailErr(t, "start GTK before the source volume", command.Start())
	_ = writer.Close()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	waited := false
	defer func() {
		_ = input.Close()
		if !waited {
			cancel()
			<-done
		}
	}()
	testutil.FailErr(t, "bound late-volume report reads", output.SetReadDeadline(time.Now().Add(15*time.Second)))
	reader := bufio.NewReader(output)
	read := func(prefix string, target any) {
		t.Helper()
		line, err := reader.ReadString('\n')
		t.Logf("late-volume protocol: %s", line)
		if err != nil {
			raw, _ := os.ReadFile(diagnostic.Name())
			t.Fatalf("late-volume report %q: %v diagnostics=%s", prefix, err, raw)
		}
		raw, ok := strings.CutPrefix(line, prefix)
		if !ok {
			t.Fatalf("unexpected late-volume report %q", line)
		}
		testutil.FailErr(t, "decode late-volume report", json.Unmarshal([]byte(raw), target))
	}
	var ready struct {
		Initialized bool `json:"gtk_initialized"`
		PID         int  `json:"pid"`
	}
	read("PWGTKREADY01 ", &ready)
	if !ready.Initialized || ready.PID <= 1 {
		t.Fatalf("mapped GTK readiness missing: %+v", ready)
	}
	if _, err := os.Stat(payload); !os.IsNotExist(err) {
		t.Fatalf("source volume arrived before readiness: %v", err)
	}
	stop := startSourceFUSE(t, volume, raw)
	defer stop()
	began := time.Now()
	_, err = fmt.Fprint(input, "r")
	testutil.FailErr(t, "request chooser read after attachment", err)
	var facts gtkVolumeFacts
	read("PWGTK01 ", &facts)
	if !facts.Initialized || !facts.ChooserRead || !facts.EngineRead {
		t.Fatalf("late chooser/engine read absent: %+v", facts)
	}
	t.Logf("GTK started before absent volume; protected chooser and engine read after FUSE attachment in %s", time.Since(began))
	_, err = fmt.Fprint(input, "h")
	testutil.FailErr(t, "retain late-volume file", err)
	read("PWGTK01 ", &facts)
	var held gtkHeldFile
	read("PWGTKHOLD01 ", &held)
	if !held.Held || held.PID != ready.PID || held.FD < 0 || held.Inode == 0 {
		t.Fatalf("late-volume held file missing: %+v", held)
	}
	_, err = fmt.Fprint(input, "c")
	testutil.FailErr(t, "release late-volume file", err)
	var closed gtkHeldFile
	read("PWGTKHOLD01 ", &closed)
	if closed.Held || closed.FD != -1 || closed.PID != held.PID || closed.Device != held.Device || closed.Inode != held.Inode {
		t.Fatalf("late-volume file release changed identity: %+v -> %+v", held, closed)
	}
	_, err = fmt.Fprint(input, "q")
	testutil.FailErr(t, "settle late-volume GTK", err)
	err = <-done
	waited = true
	testutil.FailErr(t, "late-volume GTK exit", err)
}
