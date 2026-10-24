package browser

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/osprocess"
	"github.com/lycaon/lycaon/internal/testutil"
)

const unconfinedBrowserHelperEnv = "PW_TEST_UNCONFINED_BROWSER_HELPER"

func TestLaunchHeadlessRefusesUnsafeProjectRootBeforeProvisioning(t *testing.T) {
	for _, root := range protectedBrowserRoots(t) {
		t.Run(filepath.Base(root), func(t *testing.T) {
			assertLaunchWriteRootRefused(t, LaunchOptions{
				Roots: []string{root}, CacheDir: t.TempDir(),
			}, root, confine.WriteRootCodeSecretStore)
		})
	}
	t.Run("relative", func(t *testing.T) {
		assertLaunchWriteRootRefused(t, LaunchOptions{
			Roots: []string{"relative-project"}, CacheDir: t.TempDir(),
		}, "relative-project", confine.WriteRootCodeNotAbsolute)
	})
}

func TestHeadlessLauncherPinsSubprocessPath(t *testing.T) {
	const browserPath = "/managed/chrome-headless-shell"
	args := newHeadlessLauncher(context.Background(), browserPath, "/tmp/profile", false, true).FormatArgs()

	if !slices.Contains(args, "--browser-subprocess-path="+browserPath) {
		t.Fatalf("missing explicit browser subprocess path: %v", args)
	}
	if slices.Contains(args, "--single-process") {
		t.Fatalf("browser must retain its multiprocess model: %v", args)
	}
}

// The browser's own sandbox is off only inside the host boundary.
func TestHeadlessLauncherKeepsItsSandboxWithoutTheBoundary(t *testing.T) {
	confined := newHeadlessLauncher(context.Background(), "/managed/chrome", "/tmp/profile", false, true).FormatArgs()
	if !slices.Contains(confined, "--no-sandbox") {
		t.Fatalf("confined launch must leave sandboxing to the boundary: %v", confined)
	}
	unconfined := newHeadlessLauncher(context.Background(), "/managed/chrome", "/tmp/profile", false, false).FormatArgs()
	for _, arg := range unconfined {
		if strings.HasPrefix(arg, "--no-sandbox") {
			t.Fatalf("launch without the boundary disabled the browser's sandbox: %v", unconfined)
		}
	}
}

func TestLaunchConfinedAcceptsUnconfinedPosture(t *testing.T) {
	if dir := os.Getenv(unconfinedBrowserHelperEnv); dir != "" {
		t.Setenv("LYCAON_SANDBOX", "off")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"webSocketDebuggerUrl":"ws://%s/devtools/browser/test"}`, r.Host)
		}))
		t.Cleanup(srv.Close)
		browserPIDFile := filepath.Join(dir, "browser.pid")
		helperPIDFile := filepath.Join(dir, "helper.pid")
		script := filepath.Join(dir, "fake-chrome.sh")
		body := fmt.Sprintf("#!/bin/sh\ntrap '' TERM\nsleep 60 &\necho $! > %s\necho $$ > %s\necho 'DevTools listening on ws://%s/devtools/browser/test' >&2\nexec sleep 60\n",
			strconv.Quote(helperPIDFile), strconv.Quote(browserPIDFile), srv.Listener.Addr().String())
		if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
			srv.Close()
			os.Exit(1)
		}
		l := newHeadlessLauncher(context.Background(), script, dir, true, false)
		if _, _, err := launchConfined(context.Background(), l, script, nil); err != nil {
			_, _ = io.WriteString(os.Stderr, err.Error())
			srv.Close()
			os.Exit(1)
		}
		if err := os.WriteFile(filepath.Join(dir, "ready"), []byte("1"), 0o600); err != nil {
			srv.Close()
			os.Exit(1)
		}
		time.Sleep(90 * time.Second)
		srv.Close()
		os.Exit(0)
	}

	t.Setenv("LYCAON_SANDBOX", "off")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l := newHeadlessLauncher(ctx, "/nonexistent/chrome", t.TempDir(), true, false)
	if _, _, err := launchConfined(ctx, l, "/nonexistent/chrome", nil); err == nil {
		t.Fatal("launchConfined with missing binary succeeded")
	}

	dir := t.TempDir()
	browserPIDFile := filepath.Join(dir, "browser.pid")
	helperPIDFile := filepath.Join(dir, "helper.pid")
	readyFile := filepath.Join(dir, "ready")

	self, err := os.Executable()
	testutil.FailErr(t, "locate test binary", err)
	var stderr bytes.Buffer
	engine := exec.Command(self, "-test.run=^TestLaunchConfinedAcceptsUnconfinedPosture$", "-test.v=false")
	engine.Env = append(os.Environ(), unconfinedBrowserHelperEnv+"="+dir)
	engine.Stdout = io.Discard
	engine.Stderr = &stderr
	testutil.FailErr(t, "start unconfined browser engine helper", engine.Start())
	t.Cleanup(func() {
		_ = engine.Process.Kill()
		_ = engine.Wait()
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(readyFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("unconfined browser engine helper did not become ready: %s", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	browserPID := readPIDFile(t, browserPIDFile)
	helperPID := readPIDFile(t, helperPIDFile)
	t.Cleanup(func() {
		_ = syscall.Kill(browserPID, syscall.SIGKILL)
		_ = syscall.Kill(helperPID, syscall.SIGKILL)
	})

	testutil.FailErr(t, "SIGKILL unconfined browser engine helper", engine.Process.Kill())
	_ = engine.Wait()

	killDeadline := time.Now().Add(10 * time.Second)
	for _, pid := range []int{browserPID, helperPID} {
		for osprocess.Alive(pid) {
			if time.Now().After(killDeadline) {
				t.Fatalf("unconfined browser process %d survived SIGKILLed engine", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read pid file", err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	testutil.FailErr(t, "parse pid", err)
	return pid
}
