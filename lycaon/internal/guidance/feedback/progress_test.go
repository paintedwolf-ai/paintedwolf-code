package feedback

import (
	"strings"
	"testing"
)

func TestFormatWorkflowProgressOpaqueLeaves(t *testing.T) {
	line := FormatWorkflowProgress("approve", []string{"human_approval"}, "")
	if !strings.Contains(line, "phase=approve") {
		t.Fatalf("missing phase: %q", line)
	}
	if !strings.Contains(line, "blocked=human_approval") {
		t.Fatalf("missing blocked leaf: %q", line)
	}
	if !strings.Contains(line, "next=Advance when gate satisfied") {
		t.Fatalf("missing next: %q", line)
	}
}

func TestProgressDedupKeyStable(t *testing.T) {
	wf := WorkflowEvaluationContext{
		WorkflowID: "plan", CurrentPhase: "approve", FailedLeaves: []string{"human_approval"},
	}
	a := ProgressDedupKey(wf)
	b := ProgressDedupKey(wf)
	if a == "" || a != b {
		t.Fatalf("dedup key = %q want stable non-empty", a)
	}
	for _, changed := range []WorkflowEvaluationContext{
		{WorkflowID: "review", CurrentPhase: "approve", FailedLeaves: []string{"human_approval"}},
		{WorkflowID: "plan", CurrentPhase: "verify", FailedLeaves: []string{"human_approval"}},
		{WorkflowID: "plan", CurrentPhase: "approve", FailedLeaves: []string{"tests_pass"}},
		{WorkflowID: "plan", CurrentPhase: "approve"},
	} {
		if got := ProgressDedupKey(changed); got == a {
			t.Fatalf("changed workflow progress reused dedup key %q: %+v", a, changed)
		}
	}
}
