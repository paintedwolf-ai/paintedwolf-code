package browser

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
)

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
