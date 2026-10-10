package inputs

import (
	"errors"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
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
