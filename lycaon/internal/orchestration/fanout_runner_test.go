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

// fanOutLegs is a leg store whose legs move on each read and a delegation
// that resumes a leg that ran out of budget.
type fanOutLegs struct {
	mu      sync.Mutex
	status  map[string][]api.LegStatus
	resumed []string
}

func (f *fanOutLegs) GetLeg(_ context.Context, _, legID string) (*api.Leg, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	seq := f.status[legID]
	current := seq[0]
	if len(seq) > 1 {
		f.status[legID] = seq[1:]
	}
	return &api.Leg{ID: legID, Status: current}, nil
}

func (f *fanOutLegs) ResumeLeg(_ context.Context, _, legID string) (*api.Leg, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumed = append(f.resumed, legID)
	return &api.Leg{ID: legID, Status: api.LegStatusDispatched}, nil
}

func (f *fanOutLegs) DispatchLeg(context.Context, string, string, string) (*api.Leg, error) {
	return nil, nil
}
func (f *fanOutLegs) Abort(context.Context, string, string) error { return nil }
func (f *fanOutLegs) Create(context.Context, api.Delegation, string, []api.Leg) (*api.Delegation, error) {
	return nil, nil
}
func (f *fanOutLegs) UpdateLeg(context.Context, api.Leg) error            { return nil }
func (f *fanOutLegs) ListLegs(context.Context, string) ([]api.Leg, error) { return nil, nil }
func (f *fanOutLegs) DelegationBySessionID(string) (string, bool)         { return "", false }
func (f *fanOutLegs) DelegationByWorkflowRunID(context.Context, string) (string, bool, error) {
	return "", false, nil
}

// A leg that ran out of budget resumes rather than counting as done.
func TestSettleLegsResumesBudgetExhaustedLeg(t *testing.T) {
	legs := &fanOutLegs{status: map[string][]api.LegStatus{
		"leg-0": {api.LegStatusRunning, api.LegStatusComplete},
		"leg-1": {api.LegStatusRetryPending, api.LegStatusRunning, api.LegStatusComplete},
	}}
	o := &OrchestratorImpl{store: legs, delegation: legs}
	settled, err := o.settleLegs(context.Background(), "dep-1", []string{"leg-0", "leg-1"}, TopologyBindStageFanOut)
	testutil.FailErr(t, "settle legs", err)
	if len(settled) != 2 || settled[0].Status != api.LegStatusComplete || settled[1].Status != api.LegStatusComplete {
		t.Fatalf("settled = %+v", settled)
	}
	if len(legs.resumed) != 1 || legs.resumed[0] != "leg-1" {
		t.Fatalf("resumed = %v, want [leg-1]", legs.resumed)
	}
}

func TestSettleLegsReturnsFailedLegsToTheCaller(t *testing.T) {
	legs := &fanOutLegs{status: map[string][]api.LegStatus{"leg-0": {api.LegStatusFailed}}}
	o := &OrchestratorImpl{store: legs, delegation: legs}
	settled, err := o.settleLegs(context.Background(), "dep-1", []string{"leg-0"}, TopologyBindStagePack)
	testutil.FailErr(t, "settle legs", err)
	if len(settled) != 1 || settled[0].Status != api.LegStatusFailed {
		t.Fatalf("settled = %+v", settled)
	}
}

func TestSettleLegsFailsTheStageWhenRetryIsUnavailable(t *testing.T) {
	legs := &fanOutLegs{status: map[string][]api.LegStatus{"leg-0": {api.LegStatusRetryPending}}}
	o := &OrchestratorImpl{store: legs, delegation: dispatchOnly{}}
	_, err := o.settleLegs(context.Background(), "dep-1", []string{"leg-0"}, TopologyBindStageFanOut)
	var failure *RunFailure
	if !errors.As(err, &failure) || failure.Code != RunFailureCodeRetryUnavailable || failure.Stage != TopologyBindStageFanOut {
		t.Fatalf("err = %v, want a retry-unavailable stage failure", err)
	}
}

// dispatchOnly is a delegation that cannot resume a leg.
type dispatchOnly struct{}

func (dispatchOnly) DispatchLeg(context.Context, string, string, string) (*api.Leg, error) {
	return nil, nil
}
func (dispatchOnly) Abort(context.Context, string, string) error { return nil }

func (f *fanOutLegs) Get(context.Context, string) (*api.Delegation, error) {
	return &api.Delegation{Status: api.DelegationStatusActive}, nil
}
