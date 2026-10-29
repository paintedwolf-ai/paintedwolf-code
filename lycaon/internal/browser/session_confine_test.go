package browser_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browserengine/browsertest"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/osprocess"
	"github.com/lycaon/lycaon/internal/testutil"
)

const browserEngineHelperEnv = "PW_TEST_BROWSER_ENGINE_HELPER"

// requireBrowserBoundary admits a platform whose host boundary confines the
// browser. Linux fails rather than skips when it cannot confine: the
// capability report is the finding.
func requireBrowserBoundary(t *testing.T) {
	t.Helper()
	switch runtime.GOOS {
	case "darwin":
		if !confine.Available() {
			t.Skip("seatbelt unavailable")
		}
	case "linux":
		if !confine.Available() {
			caps, _ := confine.LinuxCapabilities()
			t.Fatalf("linux confinement unavailable on this host: %+v", caps)
		}
	default:
		t.Skip("no host boundary confines the browser on this platform")
	}
}

func TestLaunchHeadlessUnderBrowserConfinement(t *testing.T) {
	if readyFile := os.Getenv(browserEngineHelperEnv); readyFile != "" {
		runBrowserEngineHelper(t, readyFile)
		return
	}
	requireBrowserBoundary(t)
	confine.TestingSetAutoConfine(t)

	browsertest.SkipIfNoBrowser(t)
	// No CacheDir: the host default is the shape the shipped app runs, and the
	// write root the profile has to allow.
	b, cleanup, err := browser.LaunchHeadless(context.Background(), browser.LaunchOptions{
		DisableJS: true,
	})
	testutil.FailErr(t, "launch confined browser", err)
	defer cleanup()
	if b == nil {
		t.Fatal("nil browser")
	}
	// The browser outlives its launch context.
	ctx, cancel := context.WithTimeout(t.Context(), browser.RasterizeTimeout)
	defer cancel()
	page, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	testutil.FailErr(t, "open page after launch", err)
	defer func() { _ = page.Context(ctx).Close() }()
	testutil.FailErr(t, "wait for screenshot page", page.Context(ctx).WaitLoad())
	raw, err := page.Context(ctx).Screenshot(false, nil)
	if err != nil {
		t.Fatalf("screenshot under the host boundary: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("empty screenshot")
	}
	cleanup()

	assertBrowserDiesWithKilledEngine(t)
}

func runBrowserEngineHelper(t *testing.T, readyFile string) {
	confine.TestingSetAutoConfine(t)
	cacheDir := filepath.Dir(readyFile)
	b, _, err := browser.LaunchHeadless(context.Background(), browser.LaunchOptions{
		CacheDir:  cacheDir,
		DisableJS: true,
	})
	if err != nil {
		_, _ = io.WriteString(os.Stderr, err.Error())
		os.Exit(1)
	}
	page, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		_, _ = io.WriteString(os.Stderr, err.Error())
		os.Exit(1)
	}
	_ = page.WaitLoad()
	if err := os.WriteFile(readyFile, []byte("ready"), 0o600); err != nil {
		os.Exit(1)
	}
	time.Sleep(90 * time.Second)
	os.Exit(0)
}

func assertBrowserDiesWithKilledEngine(t *testing.T) {
	t.Helper()
	self, err := os.Executable()
	testutil.FailErr(t, "locate test binary", err)
	cacheDir := filepath.Join(t.TempDir(), "browser-engine-loss")
	testutil.FailErr(t, "create cache dir", os.MkdirAll(cacheDir, 0o700))
	readyFile := filepath.Join(cacheDir, "ready")

	var stderr bytes.Buffer
	engine := exec.Command(self, "-test.run=^TestLaunchHeadlessUnderBrowserConfinement$", "-test.v=false")
	engine.Env = append(os.Environ(), browserEngineHelperEnv+"="+readyFile)
	engine.Stdout = io.Discard
	engine.Stderr = &stderr
	testutil.FailErr(t, "start browser engine helper", engine.Start())
	t.Cleanup(func() {
		_ = engine.Process.Kill()
		_ = engine.Wait()
		for _, pid := range browserPIDsForCache(cacheDir) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	deadline := time.Now().Add(browser.RasterizeTimeout)
	for {
		if _, err := os.Stat(readyFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("browser engine helper did not become ready: %s", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	pids := browserPIDsForCache(cacheDir)
	if len(pids) == 0 {
		t.Fatalf("browser engine helper reported ready with no live browser processes for %s", cacheDir)
	}

	testutil.FailErr(t, "SIGKILL browser engine helper", engine.Process.Kill())
	_ = engine.Wait()

	killDeadline := time.Now().Add(10 * time.Second)
	for _, pid := range pids {
		for osprocess.Alive(pid) {
			if time.Now().After(killDeadline) {
				t.Fatalf("browser process %d survived SIGKILLed engine", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if remaining := browserPIDsForCache(cacheDir); len(remaining) != 0 {
		t.Fatalf("browser processes %v survived SIGKILLed engine", remaining)
	}
}

func browserPIDsForCache(cacheDir string) []int {
	seen := map[int]bool{}
	add := func(pid int) {
		if pid <= 1 || seen[pid] {
			return
		}
		seen[pid] = true
		for _, member := range osprocess.SessionMembers(pid) {
			if member > 1 {
				seen[member] = true
			}
		}
	}
	if runtime.GOOS == "linux" {
		entries, _ := os.ReadDir("/proc")
		for _, entry := range entries {
			pid, err := strconv.Atoi(entry.Name())
			if err != nil || pid <= 1 {
				continue
			}
			raw, err := os.ReadFile("/proc/" + entry.Name() + "/cmdline")
			if err == nil && strings.Contains(string(raw), cacheDir) {
				add(pid)
			}
		}
	} else {
		out, err := exec.Command("ps", "-ax", "-o", "pid=,command=").Output()
		if err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if !strings.Contains(line, cacheDir) {
					continue
				}
				fields := strings.Fields(line)
				if len(fields) > 0 {
					if pid, err := strconv.Atoi(fields[0]); err == nil {
						add(pid)
					}
				}
			}
		}
	}
	var pids []int
	for pid := range seen {
		if osprocess.Alive(pid) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// Browser confinement permits local traffic and denies public egress.
func TestConfinedBrowserFloorReachesHostLoopback(t *testing.T) {
	requireBrowserBoundary(t)
	confine.TestingSetAutoConfine(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Host Loopback</title></head><body>ok</body></html>`))
	}))
	t.Cleanup(srv.Close)

	browsertest.SkipIfNoBrowser(t)
	b, cleanup, err := browser.LaunchHeadless(context.Background(), browser.LaunchOptions{})
	testutil.FailErr(t, "launch confined headless", err)
	defer cleanup()

	page, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	testutil.FailErr(t, "NewPage", err)
	defer func() { _ = page.Close() }()

	if err := page.Navigate(srv.URL); err != nil {
		t.Fatalf("confined browser must reach host loopback %s: %v", srv.URL, err)
	}
	_ = page.WaitLoad()
	info, err := page.Info()
	testutil.FailErr(t, "page info", err)
	if info == nil || !strings.Contains(info.Title, "Host Loopback") {
		title := ""
		if info != nil {
			title = info.Title
		}
		t.Fatalf("loopback page title = %q", title)
	}
}

func TestProbeUsabilityUnderBrowserConfinement(t *testing.T) {
	requireBrowserBoundary(t)
	confine.TestingSetAutoConfine(t)

	browsertest.SkipIfNoBrowser(t)
	testutil.FailErr(t, "probe confined browser usability", browser.ProbeUsability(
		context.Background(),
		browser.LaunchOptions{},
	))
}

func TestPoolNewPageUnderBrowserConfinement(t *testing.T) {
	requireBrowserBoundary(t)
	confine.TestingSetAutoConfine(t)

	browsertest.SkipIfNoBrowser(t)

	pool := browser.NewPool("")
	defer pool.Close()
	for i := 0; i < 2; i++ {
		page, err := pool.NewPage(context.Background(), 400, 300)
		if err != nil {
			t.Fatalf("pool NewPage #%d: %v", i, err)
		}
		_ = page.Close()
	}
}

// A path the read floor denies stays unreadable to the confined browser.
func TestConfinedBrowserCannotReadDeniedFiles(t *testing.T) {
	requireBrowserBoundary(t)
	confine.TestingSetAutoConfine(t)
	denied := t.TempDir()
	secret := filepath.Join(denied, "secret.html")
	testutil.FailErr(t, "write secret", os.WriteFile(secret, []byte(`<!doctype html><title>SECRET-CONTENT</title>`), 0o600))
	t.Setenv("LYCAON_SANDBOX_DENY_READ", denied)

	browsertest.SkipIfNoBrowser(t)
	b, cleanup, err := browser.LaunchHeadless(context.Background(), browser.LaunchOptions{})
	testutil.FailErr(t, "launch confined headless", err)
	defer cleanup()
	page, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	testutil.FailErr(t, "NewPage", err)
	defer func() { _ = page.Close() }()
	_ = page.Navigate("file://" + secret)
	_ = page.WaitLoad()
	html, _ := page.HTML()
	if strings.Contains(html, "SECRET-CONTENT") {
		t.Fatalf("confined browser read a denied file: %s", html)
	}
}
