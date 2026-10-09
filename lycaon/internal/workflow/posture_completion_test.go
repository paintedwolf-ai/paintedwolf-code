package workflow

import (
	"context"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func postureJourneyManifest(mode string) workflowdef.Manifest {
	work := workflowdef.PhaseDef{ID: "work", CompleteWhen: "gates_satisfied", Gates: []string{"ready"}, Next: "done"}
	work.Transitions = []workflowdef.PhaseTransitionDef{{ID: "finish", To: "done", Actors: []string{workflowdef.TransitionActorHuman}}}
	done := workflowdef.PhaseDef{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete", OnEnter: workflowdef.PhaseOnEnter{SetPosture: "vet"}}
	phases := []workflowdef.PhaseDef{work, done}
	if mode == "initial" {
		phases = []workflowdef.PhaseDef{done}
	}
	if mode == "final advance" {
		work.Next = ""
		work.Transitions = nil
		phases = []workflowdef.PhaseDef{work}
	}
	return workflowdef.FinalizeManifest(workflowdef.Manifest{ID: "posture-journey", Version: "1.0.0", InitialPosture: "vet", PhaseDefs: phases})
}

func finishPostureJourney(t *testing.T, mgr *RunManager, run *api.WorkflowRun, mode string) {
	t.Helper()
	ctx := t.Context()
	var err error
	switch mode {
	case "transition":
		_, err = mgr.Phases.FireTransition(ctx, run.ID, "finish", workflowdef.TransitionActorHuman)
	case "advance", "final advance":
		vars, readErr := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
		testutil.FailErr(t, "read gate state", readErr)
		testutil.FailErr(t, "satisfy gate", mgr.Store.State.UpdateVars(ctx, run, mgr.Resolver.ProjectDirForRun(ctx, run), runstate.SatisfyGateInVars(vars, "ready")))
		_, err = mgr.Phases.Advance(ctx, run.ID)
	case "failed":
		_, err = mgr.Controls.Fail(ctx, run.ID, api.WorkflowFailure{Code: "TOPOLOGY_STAGE_FAILED", Message: "Fixture failed."})
	case "interrupted":
		err = mgr.Recovery.ReconcileOrphanedRuns(ctx, run.SessionID)
	case "canceled":
		_, err = mgr.Controls.Cancel(ctx, run.ID, "fixture canceled")
	}
	testutil.FailErr(t, "finish workflow", err)
}

func TestWorkflowTerminalRestoresSessionPosture(t *testing.T) {
	for _, baseline := range []api.SessionPosture{api.SessionPostureBuild, api.SessionPostureSpec, api.SessionPostureVet} {
		for _, mode := range []string{"initial", "transition", "advance", "final advance", "failed", "canceled", "interrupted"} {
			t.Run(string(baseline)+"/"+mode, func(t *testing.T) {
				mgr, sessions, _, _ := testManager(t)
				testutil.FailErr(t, "set baseline", sessions.UpdateSession(t.Context(), "sess-1", func(s *api.Session) { s.Posture = baseline }))
				manifest := postureJourneyManifest(mode)
				mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{workflowdef.ManifestKey(manifest.ID, manifest.Version): manifest})
				assertPosture := func(ctx context.Context, want api.SessionPosture) {
					t.Helper()
					sess, err := sessions.Get(ctx, "sess-1")
					testutil.FailErr(t, "read session posture", err)
					if sess.Posture != want {
						t.Fatalf("session posture = %q, want %q", sess.Posture, want)
					}
				}
				mgr.Children.OnRunCompleted = func(ctx context.Context, _ *api.WorkflowRun) { assertPosture(ctx, baseline) }
				run, err := startRun(t.Context(), mgr, "sess-1", manifest.ID, manifest.Version)
				testutil.FailErr(t, "start workflow", err)
				if mode != "initial" {
					assertPosture(t.Context(), api.SessionPostureVet)
				}
				finishPostureJourney(t, mgr, run, mode)
				assertPosture(t.Context(), baseline)
				stored, err := mgr.Store.Runs.Get(t.Context(), run.ID)
				testutil.FailErr(t, "read terminal run", err)
				if !runstate.IsTerminal(stored.Status) {
					t.Fatalf("workflow still %s", stored.Status)
				}
			})
		}
	}
}

func TestChildTerminalRestoresParentPosture(t *testing.T) {
	for _, mode := range []string{"initial", "transition", "advance", "final advance", "failed", "canceled", "interrupted"} {
		t.Run(mode, func(t *testing.T) {
			mgr, sessions, _, _ := testManager(t)
			testutil.FailErr(t, "set baseline", sessions.UpdateSession(t.Context(), "sess-1", func(s *api.Session) { s.Posture = api.SessionPostureBuild }))
			parentManifest := postureJourneyManifest("transition")
			parentManifest.ID = "parent-posture"
			parentManifest.InitialPosture = "spec"
			childManifest := postureJourneyManifest(mode)
			mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{
				workflowdef.ManifestKey(parentManifest.ID, parentManifest.Version): parentManifest,
				workflowdef.ManifestKey(childManifest.ID, childManifest.Version):   childManifest,
			})
			parent, err := startRun(t.Context(), mgr, "sess-1", parentManifest.ID, parentManifest.Version)
			testutil.FailErr(t, "start parent", err)
			child, err := mgr.Children.InvokeChild(t.Context(), parent.ID, workflowdef.InvokeWorkflowSpec{WorkflowID: childManifest.ID, Version: childManifest.Version, Blueprint: workflowdef.ChildBlueprintNone})
			testutil.FailErr(t, "start child", err)
			finishPostureJourney(t, mgr, child, mode)
			sess, err := sessions.Get(t.Context(), "sess-1")
			testutil.FailErr(t, "read resumed parent posture", err)
			if sess.Posture != api.SessionPostureSpec {
				t.Fatalf("resumed parent posture = %q, want spec", sess.Posture)
			}
			parent, err = mgr.Store.Runs.Get(t.Context(), parent.ID)
			testutil.FailErr(t, "read resumed parent", err)
			if parent.Status != api.WorkflowRunStatusRunning {
				t.Fatalf("parent status = %s", parent.Status)
			}
			finishPostureJourney(t, mgr, parent, "transition")
			sess, err = sessions.Get(t.Context(), "sess-1")
			testutil.FailErr(t, "read restored session", err)
			if sess.Posture != api.SessionPostureBuild {
				t.Fatalf("completed parent posture = %q, want build", sess.Posture)
			}
		})
	}
}
