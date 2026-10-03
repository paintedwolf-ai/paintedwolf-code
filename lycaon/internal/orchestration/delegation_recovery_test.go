package orchestration

import (
	"context"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type recoveryLegStore struct{ *pipelineStoreStub }

func (s recoveryLegStore) DelegationByWorkflowRunID(_ context.Context, runID string) (string, bool, error) {
	for id, d := range s.delegations {
		if d.WorkflowRunID == runID {
			return id, true, nil
		}
	}
	return "", false, nil
}

func TestTopologyRecoveryReusesStoredLegs(t *testing.T) {
	for _, pattern := range []string{"fan_out", "pack"} {
		t.Run(pattern, func(t *testing.T) {
			ctx := context.Background()
			store := recoveryLegStore{newPipelineStoreStub()}
			o := &OrchestratorImpl{store: store}
			wf := &workflowRunContext{runID: "workflow-run", id: "workflow", version: "1.0.0"}
			req := RunRequest{Input: map[string]any{"project_id": "project"}}
			setup := func() (string, []string, *runState, error) {
				state := &runState{stageLegs: map[string]string{}, completed: map[string]bool{}, outputs: map[string]string{}}
				var id string
				var legs []string
				var err error
				if pattern == "fan_out" {
					id, legs, err = o.setupFanOutDelegation(ctx, req, wf, "session", "/repo", "task", "agent", FanOutSpec{Subtasks: []string{"first", "second"}}, state)
				} else {
					id, legs, err = o.setupPackDelegation(ctx, req, wf, "session", "/repo", "task", "agent", 2, nil, state)
				}
				return id, legs, state, err
			}
			id, legs, _, err := setup()
			testutil.FailErr(t, "create topology", err)
			store.legs[id][0].Status = api.LegStatusComplete
			store.legs[id][1].Status = api.LegStatusRunning
			// Storage order does not define topology work order.
			store.legs[id][0], store.legs[id][1] = store.legs[id][1], store.legs[id][0]
			resumedID, resumedLegs, state, err := setup()
			testutil.FailErr(t, "restore topology", err)
			if resumedID != id || !reflect.DeepEqual(resumedLegs, legs) || len(store.delegations) != 1 {
				t.Fatalf("recovery changed identities: %s %v; original %s %v", resumedID, resumedLegs, id, legs)
			}
			if len(state.completed) != 1 || len(state.stageLegs) != 2 {
				t.Fatalf("restored state = %+v", state)
			}
			for i, legID := range resumedLegs {
				if pattern == "fan_out" {
					err = o.dispatchFanOutLeg(ctx, req, "run", id, legID, "agent", i, wf.runID)
				} else {
					err = o.dispatchPackLeg(ctx, req, "run", id, legID, "agent", i, wf.runID)
				}
				testutil.FailErr(t, "reuse already dispatched leg", err)
			}
			store.legs[id][0].Title = "unexpected-work"
			if _, _, _, err := setup(); err == nil {
				t.Fatal("accepted incompatible stored topology")
			}
		})
	}
}
