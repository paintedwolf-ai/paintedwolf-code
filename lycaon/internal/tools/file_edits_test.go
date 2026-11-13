package tools_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func write(path, before, after string) api.FileEditSnapshot {
	return api.FileEditSnapshot{RootID: "r1", Path: path, Before: &before, After: after}
}

func create(path, after string) api.FileEditSnapshot {
	return api.FileEditSnapshot{RootID: "r1", Path: path, After: after}
}

func remove(path, before string) api.FileEditSnapshot {
	return api.FileEditSnapshot{RootID: "r1", Path: path, Before: &before, Deleted: true}
}

// describe renders an edit as path:before>after, with "-" for an absent side.
func describe(edits []api.FileEditSnapshot) string {
	var parts []string
	for _, edit := range edits {
		before, after := "-", edit.After
		if edit.Before != nil {
			before = *edit.Before
		}
		if edit.Deleted {
			after = "-"
		}
		parts = append(parts, fmt.Sprintf("%s/%s:%s>%s", edit.RootID, edit.Path, before, after))
	}
	return strings.Join(parts, " ")
}

// A result holds one edit per file, from the text before the call's first
// write to the text after its last, whatever order the writes are recorded in.
func TestRecordFileEditHoldsOneEditPerFile(t *testing.T) {
	other := write("a.go", "1", "2")
	other.RootID = "r2"
	for name, tc := range map[string]struct {
		writes []api.FileEditSnapshot
		want   string
	}{
		"files keep first-write order":       {[]api.FileEditSnapshot{write("a.go", "1", "2"), write("b.go", "x", "y"), write("a.go", "2", "3")}, "r1/a.go:1>3 r1/b.go:x>y"},
		"a later write recorded first":       {[]api.FileEditSnapshot{write("a.go", "2", "3"), write("a.go", "1", "2")}, "r1/a.go:1>3"},
		"a round trip recorded in order":     {[]api.FileEditSnapshot{write("a.go", "1", "2"), write("a.go", "2", "1")}, ""},
		"created then removed":               {[]api.FileEditSnapshot{create("tmp.txt", "x"), remove("tmp.txt", "x")}, ""},
		"removed then created":               {[]api.FileEditSnapshot{remove("a.go", "1"), create("a.go", "2")}, "r1/a.go:1>2"},
		"created then written":               {[]api.FileEditSnapshot{create("a.go", "1"), write("a.go", "1", "2")}, "r1/a.go:->2"},
		"a write that changes nothing":       {[]api.FileEditSnapshot{write("a.go", "1", "1")}, ""},
		"a change made between two writes":   {[]api.FileEditSnapshot{write("a.go", "1", "2"), write("a.go", "5", "6")}, "r1/a.go:1>2 r1/a.go:5>6"},
		"the same path in two roots":         {[]api.FileEditSnapshot{write("a.go", "1", "2"), other}, "r1/a.go:1>2 r2/a.go:1>2"},
		"a write continuing a restored file": {[]api.FileEditSnapshot{write("a.go", "1", "2"), remove("a.go", "2"), create("a.go", "3")}, "r1/a.go:1>3"},
	} {
		t.Run(name, func(t *testing.T) {
			var edits []api.FileEditSnapshot
			for _, edit := range tc.writes {
				var held bool
				if edits, held = tools.RecordFileEdit(edits, edit); !held {
					t.Fatalf("%s was not held", describe([]api.FileEditSnapshot{edit}))
				}
			}
			if got := describe(edits); got != tc.want {
				t.Fatalf("held %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRecordFileEditBoundsFilesAndText(t *testing.T) {
	out := &tools.ToolInvocationOut{}
	for i := range tools.MaxFileEdits + 3 {
		out.RecordFileEdit(create(fmt.Sprintf("%d.go", i), "x"))
	}
	if len(out.FileEdits) != tools.MaxFileEdits || out.FileEditsOmitted != 3 {
		t.Fatalf("held %d edits and omitted %d, want %d and 3", len(out.FileEdits), out.FileEditsOmitted, tools.MaxFileEdits)
	}
	// A held file keeps following its later writes at the bound.
	out.RecordFileEdit(write("0.go", "x", "y"))
	if out.FileEdits[0].After != "y" || out.FileEditsOmitted != 3 {
		t.Fatalf("a held file stopped at the bound: %s, omitted %d", describe(out.FileEdits[:1]), out.FileEditsOmitted)
	}

	large := &tools.ToolInvocationOut{}
	half := strings.Repeat("a", tools.MaxFileEditBytes/2)
	large.RecordFileEdit(create("one.txt", half))
	large.RecordFileEdit(create("two.txt", half))
	large.RecordFileEdit(create("three.txt", "x"))
	if len(large.FileEdits) != 2 || large.FileEditsOmitted != 1 {
		t.Fatalf("held %d edits and omitted %d past the text bound, want 2 and 1", len(large.FileEdits), large.FileEditsOmitted)
	}

	// Merging a child applies the same rules to the parent.
	parent := &tools.ToolInvocationOut{}
	parent.RecordFileEdit(write("a.go", "2", "3"))
	child := &tools.ToolInvocationOut{FileEditsOmitted: 2}
	child.RecordFileEdit(write("a.go", "1", "2"))
	parent.MergeChild(child, tools.MergeStageOK)
	if got := describe(parent.FileEdits); got != "r1/a.go:1>3" || parent.FileEditsOmitted != 2 {
		t.Fatalf("merged %q with %d omitted, want one composed edit and 2", got, parent.FileEditsOmitted)
	}
}
