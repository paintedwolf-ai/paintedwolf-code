package worker

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestRemainingPromoteWorkInvariants_appliedPathsDropOut(t *testing.T) {
	t.Parallel()

	assessment := PromoteAssessment{
		CleanPaths: []string{"safe.go", "also.go"},
		MergeResults: []PromoteMergeResult{
			{Path: "safe.go", Content: "safe\n"},
			{Path: "also.go", Content: "also\n"},
		},
		Conflicts: []PromoteConflict{
			{Path: "conflict.go", Reason: api.WorkerPromoteReasonThreeWayUnresolved},
		},
	}

	remaining := remainingPromoteWork(assessment, []string{"conflict.go", "safe.go"})
	if len(remaining.Conflicts) != 0 {
		t.Fatalf("conflicts = %v want none", remaining.Conflicts)
	}
	if len(remaining.CleanPaths) != 1 || remaining.CleanPaths[0] != "also.go" {
		t.Fatalf("clean paths = %v want [also.go]", remaining.CleanPaths)
	}
	if len(remaining.MergeResults) != 1 || remaining.MergeResults[0].Path != "also.go" {
		t.Fatalf("merge results = %v want also.go only", remaining.MergeResults)
	}
}

func TestRemainingPromoteWorkInvariants_emptyWhenAllApplied(t *testing.T) {
	t.Parallel()

	assessment := PromoteAssessment{
		CleanPaths: []string{"safe.go"},
		Conflicts: []PromoteConflict{
			{Path: "conflict.go", Reason: api.WorkerPromoteReasonThreeWayUnresolved},
		},
	}
	remaining := remainingPromoteWork(assessment, []string{"safe.go", "conflict.go"})
	if !mergeComplete(remaining) {
		t.Fatalf("remaining = %+v want complete", remaining)
	}
}

func TestRemainingPromoteWorkInvariants_preservesUnappliedConflicts(t *testing.T) {
	t.Parallel()

	assessment := PromoteAssessment{
		Conflicts: []PromoteConflict{
			{Path: "a.go", Reason: api.WorkerPromoteReasonThreeWayUnresolved},
			{Path: "b.go", Reason: api.WorkerPromoteReasonThreeWayUnresolved},
		},
	}
	remaining := remainingPromoteWork(assessment, []string{"a.go"})
	if len(remaining.Conflicts) != 1 || remaining.Conflicts[0].Path != "b.go" {
		t.Fatalf("remaining conflicts = %v want [b.go]", remaining.Conflicts)
	}
}
