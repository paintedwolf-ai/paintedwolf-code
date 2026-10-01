package worker

import (
	"testing"

	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadWorkersConfig(t *testing.T) {
	cfg, err := LoadWorkersConfig()
	testutil.FailErr(t, "LoadWorkersConfig failed", err)
	if cfg.Poller.MaxConcurrency != spawn.MaxInFlightTaskWorkers {
		t.Fatalf("max_concurrency = %d want %d", cfg.Poller.MaxConcurrency, spawn.MaxInFlightTaskWorkers)
	}
	if cfg.Poller.MaintenanceIntervalMs != 5000 {
		t.Fatalf("maintenance_interval_ms = %d", cfg.Poller.MaintenanceIntervalMs)
	}
}
