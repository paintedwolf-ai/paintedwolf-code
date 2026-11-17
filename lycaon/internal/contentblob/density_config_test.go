package contentblob_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/contentblob"
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
