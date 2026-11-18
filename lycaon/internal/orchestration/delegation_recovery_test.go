package orchestration

import (
	"context"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTopologyRecoveryReusesStoredUnits(t *testing.T) {
	for _, pattern := range []string{"fan_out", "pack", "pipeline"} {
		t.Run(pattern, func(t *testing.T) {
			ctx := context.Background()
			workers := NewMemoryTopologyWorkers()
			o := &OrchestratorImpl{workers: workers}
			wf := &workflowRunContext{runID: "workflow-run", id: "workflow", version: "1.0.0"}
			req := RunRequest{Input: map[string]any{"project_id": "project"}}
			setup := func() (string, []string, *runState, error) {
				state := &runState{stageUnits: map[string]string{}, completed: map[string]bool{}, outputs: map[string]string{}, workflowRunID: wf.runID}
				var id string
				var units []string
				var err error
				switch pattern {
				case "fan_out":
					id, units, err = o.setupFanOutBatch(ctx, req, wf, "session", "/repo", "task", "agent", FanOutSpec{Subtasks: []string{"first", "second"}}, state)
				case "pack":
					id, units, err = o.setupPackBatch(ctx, req, wf, "session", "/repo", "task", "agent", 2, state)
				default:
					stages := []PipelineStage{{Name: "first", AgentProfile: "agent"}, {Name: "second", AgentProfile: "agent"}}
					id, err = o.setupPipelineBatch(ctx, req, "session", "/repo", stages, state)
					units = []string{state.stageUnits["first"], state.stageUnits["second"]}
				}
				return id, units, state, err
			}
			id, unitIDs, _, err := setup()
			testutil.FailErr(t, "create topology", err)
			workers.mu.Lock()
			workers.units[id][0].Status = api.LegStatusComplete
			workers.units[id][1].Status = api.LegStatusRunning
			// Storage order does not define topology work order.
			workers.units[id][0], workers.units[id][1] = workers.units[id][1], workers.units[id][0]
			workers.mu.Unlock()
			resumedID, resumedUnits, state, err := setup()
			testutil.FailErr(t, "restore topology", err)
			if resumedID != id || !reflect.DeepEqual(resumedUnits, unitIDs) || len(workers.batches) != 1 {
				t.Fatalf("recovery changed identities: %s %v; original %s %v", resumedID, resumedUnits, id, unitIDs)
			}
			if len(state.completed) != 1 || len(state.stageUnits) != 2 {
				t.Fatalf("restored state = %+v", state)
			}
			for _, unitID := range resumedUnits {
				switch pattern {
				case "fan_out":
					err = o.dispatchFanOutUnit(ctx, id, unitID, wf.runID)
				case "pack":
					err = o.dispatchPackUnit(ctx, id, unitID, wf.runID)
				}
				testutil.FailErr(t, "reuse already dispatched unit", err)
			}
			workers.mu.Lock()
			workers.units[id][0].WorkID = "unexpected-work"
			workers.mu.Unlock()
			if _, _, _, err := setup(); err == nil {
				t.Fatal("accepted incompatible stored topology")
			}
		})
	}
}
