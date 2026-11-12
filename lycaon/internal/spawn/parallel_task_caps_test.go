package spawn_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/spawn"
)

func TestParallelTaskDefaultCapsMatchTotal(t *testing.T) {
	if spawn.DefaultMaxReadTaskWorkers != spawn.MaxInFlightTaskWorkers {
		t.Fatalf("DefaultMaxReadTaskWorkers = %d want MaxInFlightTaskWorkers %d",
			spawn.DefaultMaxReadTaskWorkers, spawn.MaxInFlightTaskWorkers)
	}
	if spawn.DefaultMaxWriteTaskWorkers != spawn.MaxInFlightTaskWorkers {
		t.Fatalf("DefaultMaxWriteTaskWorkers = %d want MaxInFlightTaskWorkers %d",
			spawn.DefaultMaxWriteTaskWorkers, spawn.MaxInFlightTaskWorkers)
	}
}

func TestPolicyTemplateVarsSizingHintsMatchStockDefaults(t *testing.T) {
	vars := spawn.PolicyTemplateVars()
	cases := map[string]int{
		"hunt_wave_workers": 8,
	}
	for key, want := range cases {
		got, ok := vars[key].(int)
		if !ok || got != want {
			t.Fatalf("%s = %v (%T) want %d", key, vars[key], vars[key], want)
		}
	}
}

func TestRefreshSizingHintVarsTracksEffectiveInFlight(t *testing.T) {
	into := map[string]any{
		"max_in_flight":     4,
		"hunt_wave_workers": 8,
	}
	spawn.RefreshSizingHintVars(into)
	if got := into["hunt_wave_workers"].(int); got != 1 {
		t.Fatalf("hunt_wave_workers = %d want 1", got)
	}
}
