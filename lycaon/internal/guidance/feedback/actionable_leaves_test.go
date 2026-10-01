package feedback

import (
	"reflect"
	"testing"
)

func TestIsWorkflowAdvance(t *testing.T) {
	if !IsWorkflowAdvance("workflow_advance") || !IsWorkflowAdvance("Workflow_Advance") {
		t.Fatal("workflow_advance unmatched")
	}
	if IsWorkflowAdvance("survey_repo") || IsWorkflowAdvance("summarize") || IsWorkflowAdvance("") {
		t.Fatal("non-advance tool matched")
	}
}

func TestActionableFailedLeavesDropsHostHITLExceptAdvance(t *testing.T) {
	leaves := []string{"human_approval", "plan_stub_valid"}
	got := ActionableFailedLeaves("write", leaves)
	want := []string{"plan_stub_valid"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("write leaves = %v want %v", got, want)
	}
	if got := ActionableFailedLeaves("read", []string{"human_approval"}); len(got) != 0 {
		t.Fatalf("read+human_approval = %v want empty", got)
	}
	got = ActionableFailedLeaves("workflow_advance", leaves)
	if !reflect.DeepEqual(got, leaves) {
		t.Fatalf("advance leaves = %v want %v", got, leaves)
	}
}

func TestActionableFailedLeavesDropsReviewLoopEvidence(t *testing.T) {
	leaves := []string{"evidence_passed:survey_challenged", "plan_stub_valid"}
	got := ActionableFailedLeaves("task", leaves)
	want := []string{"plan_stub_valid"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("task leaves = %v want %v", got, want)
	}
	if got := ActionableFailedLeaves("read", []string{"evidence_passed:options_judge"}); len(got) != 0 {
		t.Fatalf("read+evidence_passed = %v want empty", got)
	}
	got = ActionableFailedLeaves("workflow_advance", leaves)
	if !reflect.DeepEqual(got, leaves) {
		t.Fatalf("advance leaves = %v want %v", got, leaves)
	}
}
