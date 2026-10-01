package hitl

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestCheckpointToolCallIDFromContentApplyPayload(t *testing.T) {
	plan, err := CompileContentApplyPlan(ContentApplyPayload{
		Tool: "write", ToolCallID: "call-nested-1", Path: "src/a.go", After: "x",
	})
	if err != nil {
		t.Fatalf("CompileContentApplyPlan: %v", err)
	}
	stored, err := storeContentApplyPlan(plan)
	if err != nil {
		t.Fatalf("storeContentApplyPlan: %v", err)
	}
	row := StoredCheckpoint{
		Kind: api.CheckpointKindContentApply,
		Payload: map[string]any{
			"content_apply_plan": stored,
		},
	}
	if got := checkpointToolCallID(row); got != "call-nested-1" {
		t.Fatalf("checkpointToolCallID = %q want call-nested-1", got)
	}
}

func TestCheckpointToolCallIDTopLevelTakesPrecedence(t *testing.T) {
	row := StoredCheckpoint{
		Kind: api.CheckpointKindToolApproval,
		Payload: map[string]any{
			"tool_call_id": "call-top",
		},
	}
	if got := checkpointToolCallID(row); got != "call-top" {
		t.Fatalf("checkpointToolCallID = %q want call-top", got)
	}
}
