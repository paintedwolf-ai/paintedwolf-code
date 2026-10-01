package promptloop

import (
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTaskSpawnCommitted(t *testing.T) {
	if !taskSpawnCommitted("task", &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Dispatch: &api.WorkerDispatch{WorkerID: "j"}}, true) {
		t.Fatal("expected committed")
	}
	if taskSpawnCommitted("task", &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted}, true) {
		t.Fatal("expected not committed")
	}
	if taskSpawnCommitted("read", &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Dispatch: &api.WorkerDispatch{WorkerID: "j"}}, true) {
		t.Fatal("expected not committed for read")
	}
}

func TestToolRejectMessageDoesNotInferEnqueuedJobID(t *testing.T) {
	loop := &PromptLoop{}
	body := `{"agent_type":"skeptic","job_id":"job-sk","status":"enqueued"}
>>> Worker queued
Code: BANNER_TASK_QUEUED

>>> Tool feedback
Workflow gate blocked at phase adjudicate
Code: WORKFLOW_GATE_BLOCKED`
	msg := loop.toolRejectMessage("task", "call-1", "asst-1", map[string]any{"agent_type": "skeptic"},
		guidance.NewRefusal("WORKFLOW_GATE_BLOCKED", body))
	if msg.ToolResult == nil {
		t.Fatal("missing tool_result")
	}
	if msg.ToolResult.Dispatch != nil {
		t.Fatalf("dispatch must not be inferred from content: %+v", msg.ToolResult)
	}
	if msg.ToolResult.Outcome != api.ToolResultOutcomeRejected {
		t.Fatalf("outcome = %q want rejected", msg.ToolResult.Outcome)
	}
}
