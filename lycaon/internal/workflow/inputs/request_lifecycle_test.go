package inputs_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func requestTestManifest(id string, cadence workflowdef.RequestCadence, fallback string) workflowdef.Manifest {
	return workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: id, Version: "1.0.0",
		Request: &workflowdef.ManifestRequest{Cadence: cadence, Question: "What should this workflow do?", Default: fallback},
		PhaseDefs: []workflowdef.PhaseDef{{
			ID: "work", ActivityLabel: "Working", CompleteWhen: workflowdef.CompleteWhenGatesSatisfied, Gates: []string{"research_satisfied"}, Next: "done",
		}, {ID: "done", ActivityLabel: "Done", Terminal: true, CompleteWhen: "orchestration_complete"}},
	})
}

func TestWorkflowRequestUsesDefaultOnlyWhenStartIsEmpty(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	manifest := requestTestManifest("default-request", workflowdef.RequestCadenceOnce, "Review the project broadly.")
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"default-request@1.0.0": manifest})

	run, err := mgr.Starts.StartHuman(t.Context(), "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "default-request", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)
	vars, err := mgr.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	request, ok := runstate.RequestStateFromVars(vars)
	if !ok || request.Text != "Review the project broadly." || request.Source != "default" || request.Status != runstate.RequestStatusResolved {
		t.Fatalf("request = %+v ok=%v", request, ok)
	}
	resolved := mgr.Policy.ResolvedRequest(t.Context(), "sess-1")
	if resolved.RunID != run.ID || resolved.OpeningMessageID != run.StartMessageID || resolved.Text != request.Text {
		t.Fatalf("resolved request = %+v", resolved)
	}
	if runstate.FeedbackPending(vars, runstate.WorkflowRequestFeedbackID) {
		t.Fatal("default request opened a question")
	}
}

func TestAmbientEachTurnWaitsUntilSubmitThenAsksOnlyForEmpty(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	manifest := requestTestManifest("ambient-request", workflowdef.RequestCadenceEachTurn, "")
	manifest.Attach.Policy = workflowdef.AttachPolicySessionCreate
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"ambient-request@1.0.0": manifest})

	run, err := mgr.Ambient.StartAmbient(t.Context(), "sess-1", manifest.ID, manifest.Version)
	testutil.FailErr(t, "StartAmbient", err)
	vars, err := mgr.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "GetScaffoldVars after attach", err)
	request, ok := runstate.RequestStateFromVars(vars)
	if !ok || request.Status != runstate.RequestStatusWaiting || runstate.FeedbackPending(vars, runstate.WorkflowRequestFeedbackID) {
		t.Fatalf("ambient attach request = %+v vars=%v", request, vars)
	}
	ui, err := mgr.Presentation.ComputeRunUI(t.Context(), run)
	testutil.FailErr(t, "ComputeRunUI", err)
	if ui.RequestState == nil || ui.RequestState.Status != runstate.RequestStatusWaiting {
		t.Fatalf("request state UI = %+v", ui.RequestState)
	}
	if !mgr.Requests.AcceptsEmptyRequest(t.Context(), "sess-1") {
		t.Fatal("each-turn request should accept empty submit")
	}

	_, response, handled, err := mgr.Requests.PrepareUserRequest(t.Context(), "sess-1", "")
	testutil.FailErr(t, "PrepareUserRequest empty", err)
	if !handled || response == nil {
		t.Fatalf("handled=%v response=%+v", handled, response)
	}
	vars, err = mgr.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "GetScaffoldVars after empty", err)
	if !runstate.RequestPending(vars) || !runstate.FeedbackPending(vars, runstate.WorkflowRequestFeedbackID) {
		t.Fatalf("empty submit did not open request: %v", vars)
	}
}

func TestAmbientEachTurnRecordsExplicitRequestWithoutAsk(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	manifest := requestTestManifest("ambient-explicit", workflowdef.RequestCadenceEachTurn, "")
	manifest.Attach.Policy = workflowdef.AttachPolicySessionCreate
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"ambient-explicit@1.0.0": manifest})
	run, err := mgr.Ambient.StartAmbient(context.Background(), "sess-1", manifest.ID, manifest.Version)
	testutil.FailErr(t, "StartAmbient", err)

	text, response, handled, err := mgr.Requests.PrepareUserRequest(t.Context(), "sess-1", "fix the parser")
	testutil.FailErr(t, "PrepareUserRequest explicit", err)
	if text != "fix the parser" || handled || response != nil {
		t.Fatalf("text=%q handled=%v response=%+v", text, handled, response)
	}
	vars, err := mgr.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	request, ok := runstate.RequestStateFromVars(vars)
	if !ok || request.Text != "fix the parser" || request.Source != "explicit" || request.Sequence != 1 {
		t.Fatalf("request = %+v ok=%v", request, ok)
	}
}

func TestTerminalInitialPhaseWaitsForRequiredRequest(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "terminal-request", Version: "1.0.0",
		Request:   &workflowdef.ManifestRequest{Cadence: workflowdef.RequestCadenceOnce, Question: "What should this workflow do?"},
		PhaseDefs: []workflowdef.PhaseDef{{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"}},
	})
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"terminal-request@1.0.0": manifest})

	run, err := mgr.Starts.StartHuman(t.Context(), "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "terminal-request", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)
	if run.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("status before answer = %q want running", run.Status)
	}

	run, err = mgr.Requests.ResolveWorkflowRequest(t.Context(), testutil.HostOwner().ID, run.SessionID, run, manifest, "finish it")
	testutil.FailErr(t, "resolveWorkflowRequest", err)
	if run.Status != api.WorkflowRunStatusComplete {
		t.Fatalf("status after answer = %q want complete", run.Status)
	}
	vars, err := mgr.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if !runstate.RequestPhaseActive(vars) {
		t.Fatal("resolved terminal request did not activate its initial phase")
	}
}

func TestRequiredRequestPrecedesInitialPhaseFeedback(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "ordered-request", Version: "1.0.0",
		Request: &workflowdef.ManifestRequest{Cadence: workflowdef.RequestCadenceOnce, Question: "What should this workflow do?"},
		PhaseDefs: []workflowdef.PhaseDef{{
			ID: "clarify", OnEnter: workflowdef.PhaseOnEnter{RequestUserFeedback: &workflowdef.UserFeedbackPrompt{Prompt: "Which API?"}},
			CompleteWhen: workflowdef.CompleteWhenGatesSatisfied, Gates: []string{"user_feedback_received:clarify"}, Next: "done",
		}, {ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"}},
	})
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"ordered-request@1.0.0": manifest})

	run, err := mgr.Starts.StartHuman(t.Context(), "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "ordered-request", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)
	vars, err := mgr.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "GetScaffoldVars before answer", err)
	if !runstate.FeedbackPending(vars, runstate.WorkflowRequestFeedbackID) {
		t.Fatal("workflow request is not pending")
	}
	if runstate.FeedbackPending(vars, "clarify") {
		t.Fatal("initial phase feedback opened before the workflow request was answered")
	}

	run, err = mgr.Requests.ResolveWorkflowRequest(t.Context(), testutil.HostOwner().ID, run.SessionID, run, manifest, "review the API")
	testutil.FailErr(t, "resolveWorkflowRequest", err)
	vars, err = mgr.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "GetScaffoldVars after answer", err)
	if runstate.FeedbackPending(vars, runstate.WorkflowRequestFeedbackID) {
		t.Fatal("workflow request remained pending after answer")
	}
	if !runstate.FeedbackPending(vars, "clarify") {
		t.Fatal("initial phase feedback did not open after the workflow request was answered")
	}
}
