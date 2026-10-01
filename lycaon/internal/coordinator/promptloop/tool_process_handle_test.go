package promptloop

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// A yielded process reaches the wire as a typed field.
func TestApplyToolResultSidecars_stampsProcessHandle(t *testing.T) {
	tr := &api.ToolResult{Content: `{"running":true,"handle":"h-1"}`, ToolCallID: "call-1"}
	applyToolResultSidecars(
		context.Background(), nil, nil, "proj-1", "root-1", "root-1",
		"command", `{"running":true,"handle":"h-1"}`, tr,
		toolCaptures{process: &api.ToolProcessHandle{Handle: "h-1", Running: true}},
	)
	if tr.Process == nil {
		t.Fatal("tool result carries no process handle")
	}
	if tr.Process.Handle != "h-1" || !tr.Process.Running {
		t.Fatalf("process = %+v want h-1/running", tr.Process)
	}
}

// Finished calls leave the process field unset.
func TestApplyToolResultSidecars_leavesProcessUnsetWhenNothingRan(t *testing.T) {
	tr := &api.ToolResult{Content: `{"exit_code":0}`, ToolCallID: "call-1"}
	applyToolResultSidecars(
		context.Background(), nil, nil, "proj-1", "root-1", "root-1",
		"command", `{"exit_code":0}`, tr, toolCaptures{},
	)
	if tr.Process != nil {
		t.Fatalf("finished call stamped a process handle: %+v", tr.Process)
	}
}

func TestInvocationOwnerRefPrefersHandlerCapture(t *testing.T) {
	result := &api.ToolResult{Dispatch: &api.WorkerDispatch{WorkerID: "projected-job"}}
	if got := invocationOwnerRef("owner-job", result); got != "owner-job" {
		t.Fatalf("owner ref = %q", got)
	}
}

func TestApplyToolResultSidecarsRetainsResolvedSubject(t *testing.T) {
	out := &tools.ToolInvocationOut{DisplaySubject: "./task den:test:fast"}
	result := &api.ToolResult{Tool: "command_output", ToolArgs: map[string]any{"handle": "exact-handle"}}
	applyToolResultSidecars(t.Context(), nil, nil, "project", "session", "session", "command_output", "done", result, toolCapturesFrom(out))
	if result.DisplaySubject != out.DisplaySubject || result.ToolArgs["handle"] != "exact-handle" {
		t.Fatalf("resolved target or routing identity lost: %+v", result)
	}
}
