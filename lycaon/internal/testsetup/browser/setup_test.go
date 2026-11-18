package browsertestsetup

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEnableSelectsStagedBrowserWithResources(t *testing.T) {
	checkout := t.TempDir()
	t.Chdir(checkout)
	t.Setenv("PW_TEST_HOST_HOME", "")
	t.Setenv(browserengine.EnvBrowserBin, "")
	testutil.FailErr(t, "write task file", os.WriteFile(filepath.Join(checkout, "Taskfile.yml"), nil, 0o600))
	dir := filepath.Join(checkout, "lycaon-den", "src-tauri", "engine-root", "browser")
	testutil.FailErr(t, "create staged directory", os.MkdirAll(dir, 0o755))
	name := "chrome-headless-shell"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(dir, name)
	testutil.FailErr(t, "write browser fixture", os.WriteFile(bin, nil, 0o700))
	Enable()
	if got := os.Getenv(browserengine.EnvBrowserBin); got != "" {
		t.Fatalf("browser without resources selected at %q", got)
	}
	testutil.FailErr(t, "write browser resources", os.WriteFile(filepath.Join(dir, "icudtl.dat"), nil, 0o600))
	Enable()
	if got, ok := browserengine.ResolveBinary(browserengine.ResolveOptions{}); !ok || got.Path != bin {
		t.Fatalf("staged browser = %+v, want %s", got, bin)
	}

	override := filepath.Join(t.TempDir(), name)
	t.Setenv(browserengine.EnvBrowserBin, override)
	Enable()
	if got := os.Getenv(browserengine.EnvBrowserBin); got != override {
		t.Fatalf("configured browser = %q, want %q", got, override)
	}
}
