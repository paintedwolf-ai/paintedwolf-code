package confine_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Go writes local telemetry counters under the user config directory on every
// run. That directory is a default write root; the rest of the Go config
// directory is not.
func TestGoTelemetryCountersAreWritableByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// The temporary directory is a write root of its own; keep it away from home.
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(home, ".config", "paintedwolf"))
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	configDir, err := os.UserConfigDir()
	testutil.FailErr(t, "user config dir", err)
	telemetry := filepath.Join(configDir, "go", "telemetry", "local")
	testutil.FailErr(t, "create telemetry dir", os.MkdirAll(telemetry, 0o700))
	c := confine.Confinement{Roots: []string{t.TempDir()}}
	fs := confine.BoundaryOf(&c).Filesystem
	if v := fs.Verdict(confine.AccessWrite, filepath.Join(telemetry, "go.count")); !v.Allowed {
		t.Fatalf("telemetry counter write = %+v, want allowed", v)
	}
	if v := fs.Verdict(confine.AccessWrite, filepath.Join(configDir, "go", "env")); v.Allowed {
		t.Fatalf("go env write = %+v, want denied", v)
	}
}
