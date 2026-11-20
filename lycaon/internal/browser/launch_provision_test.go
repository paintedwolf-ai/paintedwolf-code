package browser

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/configlayout"
)

// A runtime launch with no staged browser refuses as a broken install; only
// explicit setup may provision the managed browser. Downloads stay skipped so a
// launch that asked to provision reports BROWSER_UNAVAILABLE without network.
func TestLaunchHeadlessWithoutBundleRefusesWithoutProvisioning(t *testing.T) {
	t.Setenv(browserengine.EnvBrowserBin, "")
	t.Setenv(browserengine.EnvBrowserSkipDownload, "1")
	t.Setenv(configlayout.EnvEngineRoot, t.TempDir())
	cacheDir := t.TempDir()

	browser, cleanup, err := LaunchHeadless(context.Background(), LaunchOptions{CacheDir: cacheDir})
	if cleanup != nil {
		cleanup()
	}
	if browser != nil {
		t.Fatal("launch without a staged browser returned a browser")
	}
	rej := &browserengine.RejectError{}
	if !errors.As(err, &rej) || rej.Code != "BROWSER_ENGINE_UNAVAILABLE" {
		t.Fatalf("err = %T %v, want a BROWSER_ENGINE_UNAVAILABLE reject", err, err)
	}
	if reason, _ := rej.Data["reason"].(string); reason != "no_bundled_browser" {
		t.Fatalf("reason = %v, want no_bundled_browser", rej.Data["reason"])
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("refused launch wrote %d entries into the managed cache", len(entries))
	}
}
