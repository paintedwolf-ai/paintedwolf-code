package orchestration

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubRunLegStore struct {
	delegationID string
	legs         []api.Leg
}

func (s stubRunLegStore) DelegationByWorkflowRunID(context.Context, string) (string, bool, error) {
	return s.delegationID, s.delegationID != "", nil
}

func (s stubRunLegStore) ListLegs(context.Context, string) ([]api.Leg, error) {
	return s.legs, nil
}

// The plan comes from the topology: bound stages in order, labelled, with the
// status of whichever legs were dispatched and pending for the rest.
func TestRunTopologyLegsProjectsPipelinePlan(t *testing.T) {
	view := TopologyLegView{
		Store: stubRunLegStore{delegationID: "dep-1", legs: []api.Leg{
			{Title: "hunt_correctness", Status: api.LegStatusComplete},
			{Title: "hunt_edges", Status: api.LegStatusRetryPending},
		}},
		Catalog: extpacks.CatalogForConsumers,
	}
	stagePhases := map[string]string{"hunt_correctness": "hunt", "hunt_edges": "hunt", "hunt_races": "hunt", "triage": "triage"}
	legs, err := view.RunTopologyLegs(context.Background(), &api.WorkflowRun{ID: "run-1"}, "bugbash", stagePhases)
	testutil.FailErr(t, "project bugbash legs", err)
	want := []api.WorkflowTopologyLeg{
		{ID: "hunt_correctness", Stage: "hunt_correctness", PhaseID: "hunt", Label: "Correctness hunt", Status: api.LegStatusComplete},
		{ID: "hunt_edges", Stage: "hunt_edges", PhaseID: "hunt", Label: "Edge case hunt", Status: api.LegStatusRetryPending},
		{ID: "hunt_races", Stage: "hunt_races", PhaseID: "hunt", Label: "Concurrency hunt", Status: api.LegStatusPending},
		{ID: "triage", Stage: "triage", PhaseID: "triage", Label: "Triage", Status: api.LegStatusPending, WaitsFor: []string{"hunt_correctness", "hunt_edges", "hunt_races"}},
	}
	if diff := cmp.Diff(want, legs); diff != "" {
		t.Fatalf("legs (-want +got):\n%s", diff)
	}
}

func TestPlannedTopologyLegsFanOutSubtasks(t *testing.T) {
	spec := &TopologySpec{FanOut: &FanOutSpec{Subtasks: []string{"Map the area", "Find constraints"}}}
	legs := plannedTopologyLegs(spec, map[string]string{TopologyBindStageFanOut: "fan_out"}, map[string]api.LegStatus{"subtask-1": api.LegStatusRunning})
	want := []api.WorkflowTopologyLeg{
		{ID: "subtask-0", Stage: TopologyBindStageFanOut, PhaseID: "fan_out", Label: "Map the area", Status: api.LegStatusPending},
		{ID: "subtask-1", Stage: TopologyBindStageFanOut, PhaseID: "fan_out", Label: "Find constraints", Status: api.LegStatusRunning},
	}
	if diff := cmp.Diff(want, legs); diff != "" {
		t.Fatalf("legs (-want +got):\n%s", diff)
	}
	if got := plannedTopologyLegs(spec, map[string]string{}, nil); got != nil {
		t.Fatalf("an unbound fan-out projected %+v", got)
	}
}

func TestParseTopologyRequiresPipelineStageLabels(t *testing.T) {
	_, err := ParseTopology([]byte("id: t\npattern: pipeline\npipeline:\n  stages:\n    - name: research\n      profile: repo-researcher\n"))
	if err == nil || !strings.Contains(err.Error(), `pipeline stage "research": label required`) {
		t.Fatalf("err = %v, want label rejection", err)
	}
}
