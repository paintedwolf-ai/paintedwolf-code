package hitl

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestContentApplyPlanComposesSelectedHostHunks(t *testing.T) {
	before := "one\nkeep a\nkeep b\nthree\n"
	after := "ONE\nkeep a\nkeep b\nTHREE\n"
	plan, err := CompileContentApplyPlan(ContentApplyPayload{
		Tool: "edit", ToolCallID: "call-1", Path: "notes.txt", Before: &before, After: after,
	})
	if err != nil {
		t.Fatalf("CompileContentApplyPlan: %v", err)
	}
	if len(plan.Hunks) != 2 {
		t.Fatalf("hunks = %+v, want two independently selectable edits", plan.Hunks)
	}

	got, err := plan.Compose(api.ContentApplyApprovePartial, []string{plan.Hunks[1].ID})
	if err != nil {
		t.Fatalf("Compose partial: %v", err)
	}
	if want := "one\nkeep a\nkeep b\nTHREE\n"; got != want {
		t.Fatalf("partial content = %q, want %q", got, want)
	}
	got, err = plan.Compose(api.ContentApplyApprove, nil)
	if err != nil {
		t.Fatalf("Compose full: %v", err)
	}
	if got != after {
		t.Fatalf("full content = %q, want %q", got, after)
	}
}

func TestContentApplyPlanRejectsClientAuthoredSelections(t *testing.T) {
	before := "before\n"
	plan, err := CompileContentApplyPlan(ContentApplyPayload{
		Tool: "write", Path: "notes.txt", Before: &before, After: "after\n",
	})
	if err != nil {
		t.Fatalf("CompileContentApplyPlan: %v", err)
	}
	if _, err := plan.Compose(api.ContentApplyApprovePartial, []string{"client-invented"}); !errors.Is(err, ErrContentApplyHunkNotFound) {
		t.Fatalf("unknown hunk err = %v", err)
	}
	if _, err := plan.Compose(api.ContentApplyApprovePartial, nil); !errors.Is(err, ErrContentApplySelectionInvalid) {
		t.Fatalf("empty selection err = %v", err)
	}
}

func TestContentApplyPlanEmitsHunkForEmptyFileCreation(t *testing.T) {
	plan, err := CompileContentApplyPlan(ContentApplyPayload{Tool: "write", Path: "empty.txt", After: ""})
	if err != nil {
		t.Fatalf("CompileContentApplyPlan: %v", err)
	}
	if len(plan.Hunks) != 1 || plan.Hunks[0].Before != "" || plan.Hunks[0].After != "" {
		t.Fatalf("empty-file hunks = %+v", plan.Hunks)
	}
}
