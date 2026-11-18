package runstate_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestIsTerminalClassifiesEveryWorkflowRunStatus(t *testing.T) {
	terminal := map[api.WorkflowRunStatus]bool{
		"running":         false,
		"paused":          false,
		"paused_on_child": false,
		"complete":        true,
		"failed":          true,
		"canceled":        true,
		"interrupted":     true,
	}
	declared := api.AllWorkflowRunStatuses()
	if len(declared) != len(terminal) {
		t.Fatalf("declared statuses = %v; classify each one here", declared)
	}
	for _, status := range declared {
		want, ok := terminal[status]
		if !ok {
			t.Fatalf("status %q has no expected classification", status)
		}
		if got := runstate.IsTerminal(status); got != want {
			t.Errorf("IsTerminal(%q) = %v want %v", status, got, want)
		}
	}
}

func TestNewBoundaryMessageUsesSystemRole(t *testing.T) {
	run := &api.WorkflowRun{ID: "run", SessionID: "session", WorkflowID: "plan", WorkflowVersion: "1.0.0"}
	msg := runstate.NewBoundaryMessage(run, "started", "research", "user request")
	if msg.Role != api.MessageRoleSystem || msg.Kind != api.MessageKindWorkflowBoundary || msg.WorkflowRunID != "run" || msg.ID == "" {
		t.Fatalf("boundary row = %+v", msg)
	}
	want := api.WorkflowBoundaryMeta{Event: "started", WorkflowID: "plan", WorkflowVersion: "1.0.0", Phase: "research", Reason: "user request"}
	if msg.WorkflowBoundary == nil || *msg.WorkflowBoundary != want {
		t.Fatalf("boundary meta = %+v want %+v", msg.WorkflowBoundary, want)
	}
}
