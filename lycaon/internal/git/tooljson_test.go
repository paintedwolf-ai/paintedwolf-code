package git_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestNormalizeToolErrorFromError(t *testing.T) {
	t.Parallel()
	got := git.NormalizeToolError(
		fmt.Errorf("git diff failed: %w", git.ErrNotRepository),
	)
	if got != "not a git repository" {
		t.Fatalf("got %q", got)
	}
}

func TestMarshalToolFailureJSON(t *testing.T) {
	t.Parallel()
	out, err := git.MarshalToolFailure(fmt.Errorf("git diff failed: %w", git.ErrNotRepository))
	if !errors.Is(err, git.ErrNotRepository) {
		t.Fatalf("serialized failure lost its cause: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if obj["available"] != false {
		t.Fatalf("payload = %v", obj)
	}
	if obj["error"] != "not a git repository" {
		t.Fatalf("error = %v", obj["error"])
	}
}

func TestMarshalDiffToolResponseSuccess(t *testing.T) {
	t.Parallel()
	out, err := git.MarshalDiffToolResponse(git.DiffToolResponse{
		Offset: 0, Limit: 80, MaxBytes: 5120,
		Files:      []git.DiffToolEntry{{Path: "a.go", Insertions: 1, Diff: "diff --git a/a.go b/a.go\n"}},
		FilesTotal: 1,
	}, nil)
	testutil.FailErr(t, "git.MarshalDiffToolResponse failed", err)
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	files, _ := obj["files"].([]any)
	if obj["available"] != true || len(files) != 1 || obj["files_total"].(float64) != 1 {
		t.Fatalf("payload = %v", obj)
	}
	if _, ok := obj["next_offset"]; ok {
		t.Fatalf("next_offset must be absent on a complete page: %v", obj)
	}
}

func TestMarshalDiffToolResponseEmptyPageKeepsFilesList(t *testing.T) {
	t.Parallel()
	out, err := git.MarshalDiffToolResponse(git.DiffToolResponse{Limit: 80}, nil)
	testutil.FailErr(t, "git.MarshalDiffToolResponse failed", err)
	var obj map[string]any
	testutil.FailErr(t, "unmarshal JSON document", json.Unmarshal([]byte(out), &obj))
	if files, ok := obj["files"].([]any); !ok || len(files) != 0 {
		t.Fatalf("files must be an empty list, got %v", obj["files"])
	}
}

func diffEntries(sizes ...int) []git.DiffToolEntry {
	out := make([]git.DiffToolEntry, 0, len(sizes))
	for i, n := range sizes {
		out = append(out, git.DiffToolEntry{Path: fmt.Sprintf("f%d.go", i), Diff: strings.Repeat("+x\n", n/3)})
	}
	return out
}

// Whole files fill the budget in order; the first file that does not fit ends
// the page, and the caller learns where to resume.
func TestFitDiffPageKeepsWholeFilesInOrder(t *testing.T) {
	t.Parallel()
	kept, cut := git.FitDiffPage(diffEntries(300, 300, 300, 300), 750)
	if len(kept) != 2 || !cut {
		t.Fatalf("kept %d files cut=%v want 2 files, cut", len(kept), cut)
	}
	if kept[0].Path != "f0.go" || kept[1].Path != "f1.go" || kept[0].DiffBytes != 300 {
		t.Fatalf("kept = %+v", kept)
	}
	for _, e := range kept {
		if e.DiffTruncated {
			t.Fatalf("whole files must not be marked truncated: %+v", e)
		}
	}
}

func TestFitDiffPageWholePageFits(t *testing.T) {
	t.Parallel()
	kept, cut := git.FitDiffPage(diffEntries(300, 300), 1000)
	if len(kept) != 2 || cut {
		t.Fatalf("kept %d cut=%v want 2 files uncut", len(kept), cut)
	}
	kept, cut = git.FitDiffPage(diffEntries(300, 300), 0)
	if len(kept) != 2 || cut {
		t.Fatalf("no budget must keep everything: kept %d cut=%v", len(kept), cut)
	}
}

// A single file over budget is still delivered, cut at a line and marked.
func TestFitDiffPageCutsAnOversizedFirstFileAtALine(t *testing.T) {
	t.Parallel()
	kept, cut := git.FitDiffPage(diffEntries(900, 30), 100)
	if len(kept) != 1 || !cut {
		t.Fatalf("kept %d cut=%v want the first file alone, cut", len(kept), cut)
	}
	first := kept[0]
	if !first.DiffTruncated || first.DiffBytes != 900 || len(first.Diff) > 100 || !strings.HasSuffix(first.Diff, "\n") {
		t.Fatalf("first = truncated %v bytes %d len %d tail %q", first.DiffTruncated, first.DiffBytes, len(first.Diff), first.Diff[len(first.Diff)-3:])
	}
	kept, cut = git.FitDiffPage(diffEntries(900), 100)
	if len(kept) != 1 || cut {
		t.Fatalf("a lone oversized file is the whole selection: kept %d cut=%v", len(kept), cut)
	}
}
