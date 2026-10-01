package browser

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
)

func TestLaunchHeadlessRefusesUnsafeProjectRootBeforeProvisioning(t *testing.T) {
	_, _, err := LaunchHeadless(context.Background(), LaunchOptions{
		Roots: []string{string(filepath.Separator)},
	})
	if !errors.Is(err, confine.ErrWriteRootRefused) {
		t.Fatalf("LaunchHeadless error = %v, want ErrWriteRootRefused", err)
	}
}

func TestHeadlessLauncherPinsSubprocessPath(t *testing.T) {
	const browserPath = "/managed/chrome-headless-shell"
	args := newHeadlessLauncher(context.Background(), browserPath, "/tmp/profile", false).FormatArgs()

	if !slices.Contains(args, "--browser-subprocess-path="+browserPath) {
		t.Fatalf("missing explicit browser subprocess path: %v", args)
	}
	if slices.Contains(args, "--single-process") {
		t.Fatalf("browser must retain its multiprocess model: %v", args)
	}
}
