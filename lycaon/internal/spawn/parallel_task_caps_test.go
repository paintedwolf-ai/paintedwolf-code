package spawn_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/spawn"
)

func TestWorkerToolBudgetEffective(t *testing.T) {
	budget := spawn.DefaultWorkerToolBudget()
	if got := budget.Effective(0); got != spawn.DefaultWorkerMaxToolLoops {
		t.Fatalf("default = %d want %d", got, spawn.DefaultWorkerMaxToolLoops)
	}
	if got := budget.Effective(8); got != 8 {
		t.Fatalf("explicit = %d want 8", got)
	}
}

// The runway notice lands before the final round and scales with the ceiling.
func TestWorkerRunwayStaysInsideTheCeiling(t *testing.T) {
	for max := 2; max <= spawn.DefaultWorkerToolBudgetMax; max++ {
		runway := spawn.WorkerRunway(max)
		if runway < 1 || runway >= max {
			t.Fatalf("WorkerRunway(%d) = %d, want within [1, %d)", max, runway, max)
		}
		if runway > 10 {
			t.Fatalf("WorkerRunway(%d) = %d, want at most 10", max, runway)
		}
		if max >= 8 && max <= 40 && runway*4 < max {
			t.Fatalf("WorkerRunway(%d) = %d, want at least a quarter of the ceiling below the cap", max, runway)
		}
	}
	if got := spawn.WorkerRunway(1); got != 0 {
		t.Fatalf("WorkerRunway(1) = %d want 0", got)
	}
}

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
	vars := spawn.PolicyTemplateVars(spawn.DefaultWorkerToolBudget())
	cases := map[string]int{
		"hunt_wave_workers":        8,
		"throwaway_tool_loops_min": 2,
		"throwaway_tool_loops_max": 4,
		"deep_tool_loops_hint":     30,
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
		"max_in_flight":              4,
		"worker_tool_budget_default": spawn.DefaultWorkerMaxToolLoops,
		"worker_tool_budget_min":     spawn.WorkerToolBudgetFloor,
		"worker_tool_budget_max":     spawn.DefaultWorkerToolBudgetMax,
		"hunt_wave_workers":          8,
	}
	spawn.RefreshSizingHintVars(into)
	if got := into["hunt_wave_workers"].(int); got != 1 {
		t.Fatalf("hunt_wave_workers = %d want 1", got)
	}
}
