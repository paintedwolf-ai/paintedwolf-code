package presentation_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestComputeRunUIChoiceTransitions(t *testing.T) {
	mgr, sessStore, _ := choiceTransitionsTestMgr(t)
	ctx := context.Background()
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	run, err := mgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "choice-transitions-fixture", WorkflowVersion: "1.0.0",
		Request: "Review the fixture and choose the next step.",
	})
	testutil.FailErr(t, "StartHuman", err)

	ui, err := mgr.Presentation.ComputeRunUI(ctx, run)
	testutil.FailErr(t, "ComputeRunUI", err)
	if ui == nil {
		t.Fatal("expected ui")
	}
	if len(ui.ChoiceTransitions) != 3 {
		t.Fatalf("choice_transitions len = %d want 3 (all human arms incl. side_quest)", len(ui.ChoiceTransitions))
	}
	want := []struct {
		id, label string
	}{
		{"deepen", "Deepen research"},
		{"critique", "Run critique"},
		{"side_quest", "Side quest"},
	}
	for i, w := range want {
		got := ui.ChoiceTransitions[i]
		if got.ID != w.id || got.Label != w.label || !got.Armed {
			t.Fatalf("[%d] = %+v want id=%q label=%q armed=true", i, got, w.id, w.label)
		}
	}
}

func TestComputeRunUIChoiceTransitionsOmitsCoordinatorOnly(t *testing.T) {
	mgr, sessStore, _ := choiceTransitionsTestMgr(t)
	ctx := context.Background()
	// Overlay a phase with a coordinator-only edge.
	m := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "choice-coord-only",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:            "decide",
			ActivityLabel: "Choosing the next step",
			Transitions: []workflowdef.PhaseTransitionDef{
				{ID: "human_arm", To: "done", Actors: []string{workflowdef.TransitionActorHuman}, Label: "Human"},
				{ID: "coord_arm", To: "done", Actors: []string{workflowdef.TransitionActorCoordinator}, Label: "Coord only"},
			},
		}, {ID: "done", ActivityLabel: "Done", Terminal: true, CompleteWhen: "orchestration_complete"}},
	})
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{workflowdef.ManifestKey(m.ID, m.Version): m})
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	run, err := mgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: m.ID, WorkflowVersion: m.Version,
	})
	testutil.FailErr(t, "StartHuman", err)
	ui, err := mgr.Presentation.ComputeRunUI(ctx, run)
	testutil.FailErr(t, "ComputeRunUI", err)
	if len(ui.ChoiceTransitions) != 1 || ui.ChoiceTransitions[0].ID != "human_arm" {
		t.Fatalf("choice_transitions = %+v want only human_arm", ui.ChoiceTransitions)
	}
}
