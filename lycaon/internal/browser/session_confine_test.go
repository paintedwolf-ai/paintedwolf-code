package browser_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/proto"
	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browserengine/browsertest"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLaunchHeadlessUnderBrowserConfinement(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Seatbelt is darwin-only")
	}
	if !confine.Available() {
		t.Skip("seatbelt unavailable")
	}
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
		t.Fatalf("screenshot under Seatbelt: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("empty screenshot")
	}
}

// Browser confinement permits local traffic and denies public egress.
func TestConfinedBrowserFloorReachesHostLoopback(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Seatbelt is darwin-only")
	}
	if !confine.Available() {
		t.Skip("seatbelt unavailable")
	}
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
	if runtime.GOOS != "darwin" {
		t.Skip("Seatbelt is darwin-only")
	}
	if !confine.Available() {
		t.Skip("seatbelt unavailable")
	}
	confine.TestingSetAutoConfine(t)

	browsertest.SkipIfNoBrowser(t)
	testutil.FailErr(t, "probe confined browser usability", browser.ProbeUsability(
		context.Background(),
		browser.LaunchOptions{},
	))
}

func TestPoolNewPageUnderBrowserConfinement(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Seatbelt is darwin-only")
	}
	if !confine.Available() {
		t.Skip("seatbelt unavailable")
	}
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
