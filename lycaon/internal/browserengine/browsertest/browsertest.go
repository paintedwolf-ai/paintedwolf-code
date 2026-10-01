// Package browsertest gates tests that launch a browser.
package browsertest

import (
	"context"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/browserengine"
)

const noBrowserMessage = "no hermetic chrome-headless-shell: run ./task browser:ensure, " +
	"or set LYCAON_BROWSER_INTEGRATION=1 to provision one"

// SkipIfNoBrowser applies the browser integration policy to a test.
func SkipIfNoBrowser(t testing.TB) {
	t.Helper()
	browserengine.TestingEnableBundledBinary()
	cacheDir := browserengine.ManagedCacheDir()
	if _, ok := browserengine.ResolveBinary(browserengine.ResolveOptions{CacheDir: cacheDir}); ok {
		return
	}
	if os.Getenv(browserengine.EnvBrowserIntegration) == "1" {
		opts := browserengine.ResolveOptions{CacheDir: cacheDir, AllowDownload: true}
		if _, err := browserengine.EnsureBinary(context.Background(), opts); err != nil {
			t.Fatalf("provision chrome-headless-shell: %v", err)
		}
		return
	}
	if os.Getenv(browserengine.EnvBrowserRequired) == "1" {
		t.Fatal(noBrowserMessage)
	}
	t.Skip(noBrowserMessage)
}
