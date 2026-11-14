package tools_test

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// A child's edits join the parent's by the rules of one result: one edit per
// file, with the count of edits past the bound carried along.
func TestMergeChildComposesFileEdits(t *testing.T) {
	text := func(value string) *string { return &value }
	parent := &tools.ToolInvocationOut{}
	parent.RecordFileEdit(api.FileEditSnapshot{RootID: "r1", Path: "a.go", Before: text("2"), After: "3"})
	child := &tools.ToolInvocationOut{FileEditsOmitted: 2}
	child.RecordFileEdit(api.FileEditSnapshot{RootID: "r1", Path: "a.go", Before: text("1"), After: "2"})
	for i := range api.MaxFileEdits {
		child.RecordFileEdit(api.FileEditSnapshot{RootID: "r1", Path: fmt.Sprintf("%d.go", i), After: "x"})
	}
	parent.MergeChild(child, tools.MergeStageOK)
	first := parent.FileEdits[0]
	if len(parent.FileEdits) != api.MaxFileEdits || first.Before == nil || *first.Before != "1" || first.After != "3" {
		t.Fatalf("merged %d edits, the first %+v; want the bound and one composed edit", len(parent.FileEdits), first)
	}
	// The child reached the bound one file early and omitted that file.
	if parent.FileEditsOmitted != 2+1 {
		t.Fatalf("omitted %d, want 3", parent.FileEditsOmitted)
	}
}
