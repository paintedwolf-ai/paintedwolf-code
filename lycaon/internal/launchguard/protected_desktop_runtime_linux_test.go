//go:build linux && protected_desktop_artifact

package launchguard

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/launchverify"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func runActualDesktop(t *testing.T, dir string, uid, gid int, groups []uint32, artifact desktopArtifact) {
	t.Helper()
	home := filepath.Join(dir, "home")
	testutil.FailErr(t, "scratch home", os.Mkdir(home, 0o700))
	testutil.FailErr(t, "scratch home owner", os.Chown(home, uid, gid))
	credential := &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: groups}
	env := []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "HOME=" + home, "XDG_SESSION_TYPE=x11"}
	defer func() {
		for _, name := range []string{"desktop.log", "vault.log", "xvfb.log", "dbus.log"} {
			raw, _ := os.ReadFile(filepath.Join(dir, name))
			t.Logf("%s:\n%s", name, raw)
		}
		for _, channel := range []string{"paintedwolf", "paintedwolf-dev"} {
			raw, _ := os.ReadFile(filepath.Join(home, ".config", channel, "engine.log"))
			t.Logf("%s engine log:\n%s", channel, raw)
		}
	}()
	// Keep the service socket private while using the real engine's fixed path.
	serviceDir := filepath.Join(dir, "run")
	testutil.FailErr(t, "service directory", os.Mkdir(serviceDir, 0o755))
	testutil.FailErr(t, "private service alias", os.Mkdir("/run/painted-wolf-code", 0o755))
	testutil.FailErr(t, "private service bind", unix.Mount(serviceDir, "/run/painted-wolf-code", "", unix.MS_BIND, ""))
	testutil.FailErr(t, "fixed service socket alias", os.Symlink("vault.sock", filepath.Join(serviceDir, "vault-identity.sock")))
	var policy GenerationPolicy
	raw, err := os.ReadFile(filepath.Join(dir, "launch.json"))
	testutil.FailErr(t, "fixture policy", err)
	testutil.FailErr(t, "fixture policy shape", json.Unmarshal(raw, &policy))
	vault := exec.Command(filepath.Join(dir, "test-driver"), "-test.run=^TestProtectedLaunchFixture$")
	vault.Env = []string{"PATH=/usr/bin:/bin", fixtureModeEnv + "=vault-server", fixtureDirEnv + "=" + dir,
		fixtureVaultBinEnv + "=" + filepath.Join(policy.GenerationRoot, "bin/pw-vault-identity"),
		fixtureUIDEnv + "=" + strconv.Itoa(uid), fixtureGIDEnv + "=" + strconv.Itoa(gid)}
	stopVault := startDesktopProcess(t, vault, filepath.Join(dir, "vault.log"))
	defer stopVault()
	awaitDesktopFile(t, filepath.Join(serviceDir, "vault.sock"))

	readDisplay, writeDisplay, err := os.Pipe()
	testutil.FailErr(t, "display number pipe", err)
	defer readDisplay.Close()
	xvfb := exec.Command("Xvfb", "-displayfd", "3", "-screen", "0", "1280x900x24", "-nolisten", "tcp")
	xvfb.Env = env
	xvfb.SysProcAttr = &syscall.SysProcAttr{Credential: credential}
	xvfb.ExtraFiles = []*os.File{writeDisplay}
	stopDisplay := startDesktopProcess(t, xvfb, filepath.Join(dir, "xvfb.log"))
	defer stopDisplay()
	_ = writeDisplay.Close()
	testutil.FailErr(t, "display readiness deadline", readDisplay.SetReadDeadline(time.Now().Add(10*time.Second)))
	display, err := bufio.NewReader(readDisplay).ReadString('\n')
	testutil.FailErr(t, "private X display readiness", err)
	env = append(env, "DISPLAY=:"+strings.TrimSpace(display))
	dbus := exec.Command("dbus-daemon", "--session", "--nofork", "--print-address=1")
	dbus.Env = env
	dbus.SysProcAttr = &syscall.SysProcAttr{Credential: credential}
	busOutput, err := dbus.StdoutPipe()
	testutil.FailErr(t, "private bus output", err)
	stopBus := startDesktopProcess(t, dbus, filepath.Join(dir, "dbus.log"))
	defer stopBus()
	address := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(busOutput).ReadString('\n'); address <- strings.TrimSpace(line) }()
	select {
	case value := <-address:
		if value == "" {
			t.Fatal("private session bus has no address")
		}
		env = append(env, "DBUS_SESSION_BUS_ADDRESS="+value)
	case <-time.After(10 * time.Second):
		t.Fatal("private session bus never became ready")
	}

	// The view contains no registered package, even when the host has one.
	queryPath, err := exec.LookPath("dpkg-query")
	testutil.FailErr(t, "package query tool", err)
	query := exec.Command(queryPath, "-S", filepath.Join(policy.LogicalInstallRoot, "bin/painted-wolf-code"))
	output, queryErr := query.CombinedOutput()
	var absent *exec.ExitError
	if !errors.As(queryErr, &absent) || absent.ExitCode() != 1 {
		t.Fatalf("fixture package registration absence was not verified: %v %s", queryErr, output)
	}
	app := exec.Command(filepath.Join(dir, "pw-launch"))
	app.Env = env
	app.SysProcAttr = &syscall.SysProcAttr{Credential: credential}
	stopApp := startDesktopProcess(t, app, filepath.Join(dir, "desktop.log"))
	defer stopApp()
	engine, desktop := awaitActualDesktopHealth(t, home, policy, app.Process.Pid)
	defer unix.Close(engine.fd)
	defer unix.Close(desktop.fd)
	enginePID, desktopPID := engine.pid, desktop.pid
	initial, err := launchverify.ReadNamespace("/proc", 0)
	testutil.FailErr(t, "initial namespace", err)
	engineNS, err := launchverify.ReadNamespace("/proc", enginePID)
	testutil.FailErr(t, "actual engine namespace", err)
	desktopNS, err := launchverify.ReadNamespace("/proc", desktopPID)
	testutil.FailErr(t, "actual desktop namespace", err)
	testutil.FailErr(t, "actual protected engine and desktop boundary", launchverify.ProtectedNamespacePair(initial, engineNS, desktopNS))
	testutil.FailErr(t, "engine birth after namespace observation", engine.current())
	testutil.FailErr(t, "desktop birth after namespace observation", desktop.current())
	t.Logf("actual Den source=%s tree=%s manifest=%s engine_pid=%d desktop_pid=%d health=passed package_registration=absent external_network=absent fixture_manifest=%s", artifact.SourceCommit, artifact.SourceTree, artifact.ManifestSHA256, enginePID, desktopPID, policy.ManifestSHA256)
	stopApp()
	if !desktopFixtureProcessExited(engine.fd, 10*time.Second) {
		t.Fatal("owned engine survived desktop teardown")
	}
}

func startDesktopProcess(t *testing.T, cmd *exec.Cmd, logPath string) func() {
	t.Helper()
	log, err := os.Create(logPath)
	testutil.FailErr(t, "owned process log", err)
	// A writer-backed transport makes exec.Cmd own and drain journal-like pipes;
	// regular inherited files are intentionally nulled by the bootstrap policy.
	sink := io.MultiWriter(log)
	cmd.Stderr = sink
	if cmd.Stdout == nil {
		cmd.Stdout = sink
	}
	cmd.WaitDelay = 10 * time.Second
	testutil.FailErr(t, "start owned desktop fixture process", cmd.Start())
	stopped := false
	return func() {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			if err := cmd.Wait(); errors.Is(err, exec.ErrWaitDelay) {
				t.Errorf("owned process output did not settle: %s", logPath)
			}
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		_ = log.Close()
	}
}

func awaitDesktopFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("desktop fixture never created %s", path)
}

func awaitActualDesktopHealth(t *testing.T, home string, policy GenerationPolicy, bootstrap int) (fixtureProcess, fixtureProcess) {
	t.Helper()
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		for _, channel := range []string{"paintedwolf", "paintedwolf-dev"} {
			var manifest struct {
				Host string `json:"host"`
				Port int    `json:"port"`
				PID  int    `json:"pid"`
			}
			raw, err := os.ReadFile(filepath.Join(home, ".config", channel, "daemon.json"))
			if err != nil {
				continue
			}
			if json.Unmarshal(raw, &manifest) != nil || manifest.PID <= 1 || manifest.Port <= 0 || manifest.Port > 65535 || !net.ParseIP(manifest.Host).IsLoopback() {
				continue
			}
			endpoint := url.URL{Scheme: "http", Host: net.JoinHostPort(manifest.Host, strconv.Itoa(manifest.Port)), Path: "/health"}
			if _, err := exec.Command("curl", "--fail", "--silent", "--max-time", "2", endpoint.String()).Output(); err != nil {
				continue
			}
			pinnedEngine, err := pinDesktopFixtureProcess(manifest.PID, 0)
			if err != nil {
				continue
			}
			keepHandles := false
			desktopFD := -1
			defer func() {
				if !keepHandles {
					_ = unix.Close(pinnedEngine.fd)
					if desktopFD >= 0 {
						_ = unix.Close(desktopFD)
					}
				}
			}()
			testutil.FailErr(t, "engine belongs to owned bootstrap", fixtureDescendsFrom(manifest.PID, bootstrap))
			engine, err := os.Stat(fmt.Sprintf("/proc/%d/exe", manifest.PID))
			testutil.FailErr(t, "running engine image", err)
			selected, err := os.Stat(filepath.Join(policy.GenerationRoot, "bin/pw"))
			testutil.FailErr(t, "selected engine image", err)
			if !os.SameFile(engine, selected) {
				t.Fatal("healthy process is not the selected engine")
			}
			status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", manifest.PID))
			testutil.FailErr(t, "engine parent", err)
			parent := 0
			for _, line := range strings.Split(string(status), "\n") {
				if strings.HasPrefix(line, "PPid:") {
					parent, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PPid:")))
				}
			}
			if parent != pinnedEngine.parent {
				t.Fatal("engine parent changed during observation")
			}
			pinnedDesktop, err := pinDesktopFixtureProcess(parent, 0)
			testutil.FailErr(t, "retain actual desktop birth", err)
			desktopFD = pinnedDesktop.fd
			desktop, err := os.Stat(fmt.Sprintf("/proc/%d/exe", parent))
			testutil.FailErr(t, "running desktop image", err)
			selected, err = os.Stat(filepath.Join(policy.GenerationRoot, "bin/painted-wolf-code"))
			testutil.FailErr(t, "selected desktop image", err)
			if !os.SameFile(desktop, selected) {
				t.Fatal("engine parent is not the actual selected Den")
			}
			testutil.FailErr(t, "engine birth remains current", pinnedEngine.current())
			testutil.FailErr(t, "desktop birth remains current", pinnedDesktop.current())
			keepHandles = true
			return pinnedEngine, pinnedDesktop
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("actual protected Den never served engine health with an empty package database")
	return fixtureProcess{}, fixtureProcess{}
}
