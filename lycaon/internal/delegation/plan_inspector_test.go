package delegation

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"testing"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type planStub struct {
	approved bool
	plan     *api.Blueprint
}

func (p planStub) Get(_ context.Context, _ string) (*api.Blueprint, error) {
	return p.plan, nil
}

func (p planStub) IsApproved(_ context.Context, _, _ string) (bool, error) {
	return p.approved, nil
}

func TestPlanApprovalDispatchGate(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	delegation := api.Delegation{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), Task: "t", BlueprintPath: "plan-1"}
	leg := api.Leg{ID: "leg-1", Title: "t", Prompt: "t"}
	created, err := store.Create(ctx, delegation, "sess", []api.Leg{leg})
	testutil.FailErr(t, "create session in store", err)
	gate := PlanApprovalDispatchGate{
		Inner: AllowGate{},
		Store: store,
		Plans: planStub{approved: false},
	}
	ok, reason, err := gate.Check(ctx, created.ID, leg.ID)
	if err != nil || ok || reason != "plan not approved" {
		t.Fatalf("ok=%v reason=%q err=%v", ok, reason, err)
	}
	gate.Plans = planStub{approved: true}
	ok, _, err = gate.Check(ctx, created.ID, leg.ID)
	if err != nil || !ok {
		t.Fatalf("expected approved dispatch ok=%v err=%v", ok, err)
	}
}

func TestImplementWorkflowDispatchGate(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	delegation := api.Delegation{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), Task: "t", BlueprintPath: "plan-1", CoordinatorSessionID: "sess-1"}
	leg := api.Leg{ID: "leg-1", Title: "t", Prompt: "t"}
	created, err := store.Create(ctx, delegation, "sess-1", []api.Leg{leg})
	testutil.FailErr(t, "create session in store", err)
	ready := &workflowReadyStub{}
	gate := ImplementWorkflowDispatchGate{
		Inner:         AllowGate{},
		Store:         store,
		Plans:         planStub{approved: true},
		WorkflowReady: ready,
	}
	ok, reason, err := gate.Check(ctx, created.ID, leg.ID)
	if err != nil || ok || reason != "implement workflow not ready" {
		t.Fatalf("ok=%v reason=%q err=%v", ok, reason, err)
	}
	ready.ready = true
	ok, _, err = gate.Check(ctx, created.ID, leg.ID)
	if err != nil || !ok {
		t.Fatalf("expected ready dispatch ok=%v err=%v", ok, err)
	}
}

type workflowReadyStub struct {
	ready bool
}

func (w workflowReadyStub) ImplementWorkflowReady(_ context.Context, _, _ string) (bool, error) {
	return w.ready, nil
}

func TestLegsFromPlanCriteria(t *testing.T) {
	p := &api.Blueprint{
		Content: `<!-- lycaon:tasks
[{"id":"t1","title":"Auth","files":["a.go"],"verify":["go test ./a/..."]}]
-->`,
	}
	legs := LegsFromPlan(p, blueprint.ExtractTasks(p.Content))
	if len(legs) != 1 {
		t.Fatalf("legs = %d", len(legs))
	}
	found := false
	for _, c := range legs[0].CompletionCriteria {
		if c == "file:modified:a.go" || c == "test:pass:go test ./a/..." {
			found = true
		}
	}
	if !found {
		t.Fatalf("criteria = %v", legs[0].CompletionCriteria)
	}
}
