package orchestration

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFanOutEmptySubtasksFails(t *testing.T) {
	err := validateFanOutSpec(FanOutSpec{Subtasks: nil})
	if err == nil {
		t.Fatal("expected error for empty subtasks")
	}
}

func TestFanOutSubtasksExceedMaxWorkersFails(t *testing.T) {
	subtasks := make([]string, 6)
	for i := range subtasks {
		subtasks[i] = "task"
	}
	err := validateFanOutSpec(FanOutSpec{Subtasks: subtasks, MaxWorkers: 5})
	if err == nil {
		t.Fatal("expected max_workers error")
	}
}

func TestFanOutUnknownAggregationFails(t *testing.T) {
	err := validateFanOutSpec(FanOutSpec{
		Subtasks:    []string{"one"},
		Aggregation: "invalid",
	})
	if err == nil {
		t.Fatal("expected aggregation error")
	}
}

func TestEffectiveFanOutProfileDefaultsPathExplorer(t *testing.T) {
	if got := effectiveFanOutProfile(FanOutSpec{}); got != ProfilePathExplorer {
		t.Fatalf("profile = %q", got)
	}
}

func TestLoadFanOutReconTopology(t *testing.T) {
	spec, err := LoadTopologyFromFile(bundledTopologyPath(t, "fan-out-recon.yaml"))
	testutil.FailErr(t, "LoadTopologyFromFile failed", err)
	if spec.Pattern != TopologyFanOut {
		t.Fatalf("pattern = %q", spec.Pattern)
	}
	if spec.FanOut == nil {
		t.Fatal("missing fan_out spec")
	}
	if spec.FanOut.ProfileID != ProfilePathExplorer {
		t.Fatalf("profile = %q", spec.FanOut.ProfileID)
	}
	if len(spec.FanOut.Subtasks) != 3 {
		t.Fatalf("subtasks = %d", len(spec.FanOut.Subtasks))
	}
	if spec.FanOut.Aggregation != AggregationMerge {
		t.Fatalf("aggregation = %q", spec.FanOut.Aggregation)
	}
}

// sequencedUnits advances each unit's status on successive reads.
type sequencedUnits struct {
	*MemoryTopologyWorkers
	mu     sync.Mutex
	status map[string][]api.LegStatus
}

func (s *sequencedUnits) GetUnit(_ context.Context, _, unitID string) (*TopologyUnit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq := s.status[unitID]
	current := seq[0]
	if len(seq) > 1 {
		s.status[unitID] = seq[1:]
	}
	return &TopologyUnit{ID: unitID, Status: current}, nil
}

func TestSettleUnitsWaitsUntilAllUnitsSettle(t *testing.T) {
	units := &sequencedUnits{
		MemoryTopologyWorkers: NewMemoryTopologyWorkers(),
		status: map[string][]api.LegStatus{
			"unit-0": {api.LegStatusDispatched, api.LegStatusRunning, api.LegStatusComplete},
			"unit-1": {api.LegStatusRunning, api.LegStatusRunning, api.LegStatusComplete},
		},
	}
	o := &OrchestratorImpl{workers: units}
	settled, err := o.settleUnits(context.Background(), "batch-1", []string{"unit-0", "unit-1"})
	testutil.FailErr(t, "settle units", err)
	if len(settled) != 2 || settled[0].Status != api.LegStatusComplete || settled[1].Status != api.LegStatusComplete {
		t.Fatalf("settled = %+v", settled)
	}
}

func TestSettleUnitsReturnsFailedUnitsToCaller(t *testing.T) {
	units := &sequencedUnits{
		MemoryTopologyWorkers: NewMemoryTopologyWorkers(),
		status:                map[string][]api.LegStatus{"unit-0": {api.LegStatusFailed}},
	}
	o := &OrchestratorImpl{workers: units}
	settled, err := o.settleUnits(context.Background(), "batch-1", []string{"unit-0"})
	testutil.FailErr(t, "settle units", err)
	if len(settled) != 1 || settled[0].Status != api.LegStatusFailed {
		t.Fatalf("settled = %+v", settled)
	}
}

func TestSettleUnitsHonorsContextCancellation(t *testing.T) {
	units := &sequencedUnits{
		MemoryTopologyWorkers: NewMemoryTopologyWorkers(),
		status:                map[string][]api.LegStatus{"unit-0": {api.LegStatusRunning}},
	}
	o := &OrchestratorImpl{workers: units}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := o.settleUnits(ctx, "batch-1", []string{"unit-0"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
