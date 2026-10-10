package inputs

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestAskReceiptReplayPreservesPromptAndRejectsChangedInput(t *testing.T) {
	ask := runstate.CoordinatorAsk{ID: "ask", RunID: "run", State: runstate.CoordinatorAskPending, ToolCallID: "operation", InputDigest: "digest", Prompt: "Review retained evidence", ResponseType: workflowdef.FeedbackResponseSingleChoice, Options: []string{"Approve", "Reject"}, ArtifactIDs: []string{"first", "second"}, IssuedRevision: 3}
	vars := runstate.SetCoordinatorAsk(nil, ask)
	handle, ok, err := replayUserInputFromVars(vars, "run", "operation", "digest")
	if err != nil || !ok || handle.Prompt != ask.Prompt || handle.IssuedRevision != 3 {
		t.Fatalf("replayed handle=%+v ok=%v err=%v", handle, ok, err)
	}
	_, _, err = replayUserInputFromVars(vars, "run", "operation", "changed")
	var reject *AskUserReject
	if !errors.As(err, &reject) || reject.Code != "ASK_USER_OPERATION_CONFLICT" {
		t.Fatalf("changed input replay=%v", err)
	}
	id, prompt, pending := pendingCoordinatorAskFromVars(vars)
	if !pending || id != "ask" || prompt.Prompt != ask.Prompt || len(prompt.ArtifactIDs) != 2 {
		t.Fatalf("pending projection=%+v id=%s", prompt, id)
	}
	prompt.Options[0] = "mutated"
	prompt.ArtifactIDs[0] = "mutated"
	_, again, _ := pendingCoordinatorAskFromVars(vars)
	if again.Options[0] != "Approve" || again.ArtifactIDs[0] != "first" {
		t.Fatalf("projection mutated receipt=%+v", again)
	}
	for _, v := range []map[string]any{
		{"user_feedback": map[string]any{"phase": map[string]any{"pending": true}}},
		{"user_decision": map[string]any{"ignore": "invalid", "phase": map[string]any{"pending": true}}},
	} {
		data := pendingAskRejectData(v)
		if data["phase_id"] != "phase" || data["pending_input_id"] != "phase" {
			t.Fatalf("pending refusal context=%v", data)
		}
	}
	if pendingAskRejectData(nil) != nil {
		t.Fatal("absent pending input synthesized refusal context")
	}
}

type scaffoldFixture struct{ vars map[string]any }

func (s *scaffoldFixture) GetVars(context.Context, string) (map[string]any, error) {
	return s.vars, nil
}
func (s *scaffoldFixture) UpsertVars(_ context.Context, _ string, vars map[string]any) error {
	s.vars = vars
	return nil
}
func TestDeferredBlueprintLaunchIsConsumedOnceWithoutLosingScaffold(t *testing.T) {
	store := &scaffoldFixture{vars: map[string]any{"retained": "fact"}}
	service := &Scaffold{Store: store}
	if err := service.NoteWorkflowStartProposal(t.Context(), "session", "catalog", "1.0.0"); err != nil {
		t.Fatalf("record start proposal: %v", err)
	}
	if err := service.SetPendingBlueprintLaunchPath(t.Context(), "session", "plans/launch.md"); err != nil {
		t.Fatalf("record deferred path: %v", err)
	}
	if path := service.TakePendingBlueprintLaunchPath(t.Context(), "session"); path != "plans/launch.md" {
		t.Fatalf("deferred path=%q", path)
	}
	if path := service.TakePendingBlueprintLaunchPath(t.Context(), "session"); path != "" {
		t.Fatalf("deferred path replay=%q", path)
	}
	service.ClearStartState(t.Context(), "session")
	if store.vars["retained"] != "fact" {
		t.Fatalf("scaffold clearing lost unrelated fact=%v", store.vars)
	}
}

type feedbackReceiptRuns struct {
	runstate.RunsRepository
	vars   map[string]any
	active *api.WorkflowRun
}

func (r feedbackReceiptRuns) ActiveBySession(context.Context, string) (*api.WorkflowRun, error) {
	return r.active, nil
}
func (r feedbackReceiptRuns) GetScaffoldVars(context.Context, string) (map[string]any, error) {
	return r.vars, nil
}
func TestFeedbackToolReturnsRetainedInputWithoutIssuingAnotherQuestion(t *testing.T) {
	for _, pending := range []bool{false, true} {
		runs := feedbackReceiptRuns{}
		if pending {
			runs.active = &api.WorkflowRun{ID: "run"}
			runs.vars = map[string]any{"user_feedback": map[string]any{"phase": map[string]any{"pending": true, "prompt": "Retained question"}}}
		}
		reg := tools.NewDefaultRegistry()
		if err := RegisterFeedbackTool(reg, &Feedback{Runs: runs}); err != nil {
			t.Fatalf("register feedback: %v", err)
		}
		out, err := reg.Run(t.Context(), "workflow_user_feedback", nil, tools.ToolContext{Identity: tools.InvocationIdentity{Agent: "coordinator", SessionID: "session"}})
		if err != nil {
			t.Fatalf("project pending input: %v", err)
		}
		var result FeedbackToolResult
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("decode feedback projection: %v", err)
		}
		if result.Pending != pending || (pending && result.PhaseID != "phase") {
			t.Fatalf("feedback projection=%+v pending=%v", result, pending)
		}
	}
}
