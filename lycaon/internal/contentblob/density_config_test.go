package contentblob_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/contentblob"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDefaultDensityConfig(t *testing.T) {
	cfg := contentblob.DefaultDensityConfig()
	if cfg.IdleThreshold() <= 0 {
		t.Fatalf("IdleThreshold = %v, want > 0", cfg.IdleThreshold())
	}
	if cfg.PollInterval() <= 0 {
		t.Fatalf("PollInterval = %v, want > 0", cfg.PollInterval())
	}
	if cfg.ProjectsPerTick() <= 0 {
		t.Fatalf("ProjectsPerTick = %d, want > 0", cfg.ProjectsPerTick())
	}
	if cfg.BlobsPerProjectTick() <= 0 {
		t.Fatalf("BlobsPerProjectTick = %d, want > 0", cfg.BlobsPerProjectTick())
	}
}

func TestLoadDensityOverlay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "density.yaml")
	if err := os.WriteFile(path, []byte(`
density:
  idle_threshold_days: 30
  projects_per_tick: 5
`), 0o644); err != nil {
		testutil.FailErr(t, "write density overlay", err)
	}
	cfg, err := contentblob.LoadDensityOverlay(path)
	testutil.FailErr(t, "contentblob.LoadDensityOverlay failed", err)
	if got, want := cfg.IdleThreshold().Hours()/24, 30.0; got != want {
		t.Fatalf("IdleThreshold days = %v, want %v", got, want)
	}
	if got, want := cfg.ProjectsPerTick(), 5; got != want {
		t.Fatalf("ProjectsPerTick = %d, want %d", got, want)
	}
	// Unset fields fall back to bundled defaults, not zero.
	if cfg.BlobsPerProjectTick() <= 0 {
		t.Fatalf("BlobsPerProjectTick = %d, want > 0 (fallback to default)", cfg.BlobsPerProjectTick())
	}
}

func TestLoadDensityOverlayMissingFile(t *testing.T) {
	if _, err := contentblob.LoadDensityOverlay(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected error for missing overlay file")
	}
}
