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
	got := git.NormalizeToolError(
		fmt.Errorf("git diff failed: %w", git.ErrNotRepository),
	)
	if got != "not a git repository" {
		t.Fatalf("got %q", got)
	}
}

func TestMarshalToolFailureJSON(t *testing.T) {
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
	out, err := git.MarshalDiffToolResponse(git.DiffToolResponse{Limit: 80}, nil)
	testutil.FailErr(t, "git.MarshalDiffToolResponse failed", err)
	var obj map[string]any
	testutil.FailErr(t, "unmarshal JSON document", json.Unmarshal([]byte(out), &obj))
	if files, ok := obj["files"].([]any); !ok || len(files) != 0 {
		t.Fatalf("files must be an empty list, got %v", obj["files"])
	}
}

const twoFileDiff = "diff --git a/a.go b/a.go\nindex 1..2 100644\n--- a/a.go\n+++ b/a.go\n@@ -1 +1,2 @@\n line\n+added\ndiff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1,2 +1 @@\n-gone\n line\n"

// Hunk prefixes distinguish content containing a header string from a file header.
func TestSplitDiffBlocksSplitsAtFileHeaders(t *testing.T) {
	text := twoFileDiff + "diff --git a/c.txt b/c.txt\n--- a/c.txt\n+++ b/c.txt\n@@ -1 +1 @@\n+diff --git a/x b/x mentioned inside a hunk\n"
	blocks := git.SplitDiffBlocks(text)
	if len(blocks) != 3 {
		t.Fatalf("blocks = %d want 3: %q", len(blocks), blocks)
	}
	for i, want := range []string{"diff --git a/a.go b/a.go\n", "diff --git a/b.go b/b.go\n", "diff --git a/c.txt b/c.txt\n"} {
		if !strings.HasPrefix(blocks[i], want) {
			t.Fatalf("block %d = %q want prefix %q", i, blocks[i], want)
		}
	}
	if strings.Join(blocks, "") != text {
		t.Fatal("blocks must partition the text without loss")
	}
	if got := git.SplitDiffBlocks(""); len(got) != 0 {
		t.Fatalf("empty diff must split to no blocks, got %d", len(got))
	}
}

func TestCountHunkLinesSkipsFileHeaders(t *testing.T) {
	blocks := git.SplitDiffBlocks(twoFileDiff)
	if ins, del := git.CountHunkLines(blocks[0]); ins != 1 || del != 0 {
		t.Fatalf("a.go counts = +%d -%d want +1 -0", ins, del)
	}
	if ins, del := git.CountHunkLines(blocks[1]); ins != 0 || del != 1 {
		t.Fatalf("b.go counts = +%d -%d want +0 -1", ins, del)
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
	kept, cut := git.FitDiffPage(diffEntries(300, 300), 1000)
	if len(kept) != 2 || cut {
		t.Fatalf("kept %d cut=%v want 2 files uncut", len(kept), cut)
	}
	kept, cut = git.FitDiffPage(diffEntries(300, 300), 0)
	if len(kept) != 2 || cut {
		t.Fatalf("no budget must keep everything: kept %d cut=%v", len(kept), cut)
	}
}

// A single file over budget is still delivered — cut at a line and marked —
// rather than producing an empty page the caller could never get past.
func TestFitDiffPageCutsAnOversizedFirstFileAtALine(t *testing.T) {
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

func TestMarshalStatusToolResponseCarriesBranchFacts(t *testing.T) {
	status := &git.GitStatus{Branch: "main", HeadShort: "c656e1a0", Upstream: "origin/main", Ahead: 2, Behind: 1, Files: []git.GitStatusEntry{}}
	obj := decodeStatusTool(t, status, git.StatusToolPage{Limit: 80})
	if obj["head_short"] != "c656e1a0" || obj["upstream"] != "origin/main" || obj["ahead"].(float64) != 2 || obj["behind"].(float64) != 1 {
		t.Fatalf("branch facts = %v", obj)
	}
}

// group_depth rolls the selection up by leading segments, largest first, with
// short paths as their own group and no groups until asked for.
func TestMarshalStatusToolResponseGroupsByDepth(t *testing.T) {
	status := &git.GitStatus{Branch: "main", Dirty: true, Files: []git.GitStatusEntry{
		{Path: "lycaon/internal/scan/a.go", Status: " M"},
		{Path: "lycaon/internal/scan/b.go", Status: "M "},
		{Path: "lycaon/internal/scan/c.go", Status: "??"},
		{Path: "lycaon/internal/git/d.go", Status: " M"},
		{Path: "docs/git.md", Status: " M"},
		{Path: "Taskfile.yml", Status: " M"},
	}}
	plain := decodeStatusTool(t, status, git.StatusToolPage{Limit: 80})
	if _, ok := plain["groups"]; ok {
		t.Fatalf("groups must be absent without group_depth: %v", plain["groups"])
	}
	obj := decodeStatusTool(t, status, git.StatusToolPage{Limit: 80, GroupDepth: 3})
	groups, _ := obj["groups"].([]any)
	if obj["group_depth"].(float64) != 3 || obj["groups_total"].(float64) != 4 || len(groups) != 4 {
		t.Fatalf("groups receipt = depth %v total %v len %d", obj["group_depth"], obj["groups_total"], len(groups))
	}
	first := groups[0].(map[string]any)
	if first["prefix"] != "lycaon/internal/scan" || first["files"].(float64) != 3 || first["staged"].(float64) != 1 || first["unstaged"].(float64) != 2 || first["untracked"].(float64) != 1 {
		t.Fatalf("largest group = %v", first)
	}
	order := make([]string, 0, len(groups))
	for _, g := range groups {
		order = append(order, g.(map[string]any)["prefix"].(string))
	}
	want := []string{"lycaon/internal/scan", "Taskfile.yml", "docs/git.md", "lycaon/internal/git"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("group order = %v want %v (largest first, then by prefix)", order, want)
	}
	depth1 := decodeStatusTool(t, status, git.StatusToolPage{Limit: 80, GroupDepth: 1})
	if depth1["groups_total"].(float64) != 3 {
		t.Fatalf("depth 1 groups_total = %v want 3", depth1["groups_total"])
	}
	bounded := decodeStatusTool(t, status, git.StatusToolPage{Limit: 2, GroupDepth: 3})
	if g, _ := bounded["groups"].([]any); len(g) != 2 || bounded["groups_truncated"] != true {
		t.Fatalf("groups must honour the page limit: %v", bounded)
	}
}

func TestMarshalStatusToolResponseUnavailable(t *testing.T) {
	out, err := git.MarshalStatusToolResponse(git.StatusToolResponse{}, fmt.Errorf("git status failed: %w", git.ErrNotRepository))
	if !errors.Is(err, git.ErrNotRepository) {
		t.Fatalf("status failure lost typed cause: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if obj["available"] != false || obj["error"] != "not a git repository" {
		t.Fatalf("payload = %v", obj)
	}
}

func statusToolFixture(n int) *git.GitStatus {
	files := make([]git.GitStatusEntry, n)
	for i := range files {
		files[i] = git.GitStatusEntry{Path: fmt.Sprintf("src/f%03d.go", i), Status: " M"}
	}
	return &git.GitStatus{Branch: "main", Dirty: true, UnstagedCount: n, Files: files}
}

func decodeStatusTool(t *testing.T, status *git.GitStatus, page git.StatusToolPage) map[string]any {
	t.Helper()
	out, err := git.MarshalStatusToolResponse(git.BuildStatusToolResponse(status, page), nil)
	testutil.FailErr(t, "git.MarshalStatusToolResponse failed", err)
	var obj map[string]any
	testutil.FailErr(t, "unmarshal JSON document", json.Unmarshal([]byte(out), &obj))
	return obj
}

func statusToolPaths(obj map[string]any) []string {
	files, _ := obj["files"].([]any)
	out := make([]string, 0, len(files))
	for _, f := range files {
		entry, _ := f.(map[string]any)
		out = append(out, entry["path"].(string))
	}
	return out
}

// Every file is reachable: a page carries its total and the next offset, and
// walking next_offset visits each entry exactly once.
func TestMarshalStatusToolResponsePagesEveryFile(t *testing.T) {
	status := statusToolFixture(231)
	first := decodeStatusTool(t, status, git.StatusToolPage{Limit: 80})
	if got := len(statusToolPaths(first)); got != 80 {
		t.Fatalf("first page files = %d want 80", got)
	}
	if first["files_total"].(float64) != 231 || first["files_truncated"] != true || first["next_offset"].(float64) != 80 {
		t.Fatalf("first page receipt = total %v truncated %v next %v", first["files_total"], first["files_truncated"], first["next_offset"])
	}
	if first["offset"].(float64) != 0 || first["limit"].(float64) != 80 {
		t.Fatalf("page echo = offset %v limit %v", first["offset"], first["limit"])
	}
	seen := map[string]int{}
	offset := 0
	for pages := 0; ; pages++ {
		obj := decodeStatusTool(t, status, git.StatusToolPage{Offset: offset, Limit: 80})
		for _, p := range statusToolPaths(obj) {
			seen[p]++
		}
		next, more := obj["next_offset"].(float64)
		if !more {
			if obj["files_truncated"] != false {
				t.Fatalf("last page must not be truncated: %v", obj)
			}
			break
		}
		offset = int(next)
		if pages > 10 {
			t.Fatal("paging did not terminate")
		}
	}
	if len(seen) != 231 {
		t.Fatalf("visited %d distinct files want 231", len(seen))
	}
	for p, n := range seen {
		if n != 1 {
			t.Fatalf("%s visited %d times", p, n)
		}
	}
}

func TestMarshalStatusToolResponseOffsetPastEndIsEmptyPage(t *testing.T) {
	obj := decodeStatusTool(t, statusToolFixture(5), git.StatusToolPage{Offset: 50, Limit: 80})
	if got := len(statusToolPaths(obj)); got != 0 {
		t.Fatalf("files = %d want 0", got)
	}
	if obj["files_total"].(float64) != 5 || obj["files_truncated"] != false {
		t.Fatalf("receipt = %v", obj)
	}
	if _, ok := obj["next_offset"]; ok {
		t.Fatalf("next_offset must be absent past the end: %v", obj)
	}
	if _, ok := obj["files"]; !ok {
		t.Fatalf("files must be present as an empty list: %v", obj)
	}
}

func TestMarshalStatusToolResponseUnboundedWhenLimitZero(t *testing.T) {
	obj := decodeStatusTool(t, statusToolFixture(231), git.StatusToolPage{})
	if got := len(statusToolPaths(obj)); got != 231 {
		t.Fatalf("files = %d want 231", got)
	}
	if obj["files_truncated"] != false {
		t.Fatalf("files_truncated = %v", obj["files_truncated"])
	}
}

// paths selects by repo-relative prefix: a file exactly, or a directory and its
// subtree — never a bare string prefix of a sibling name.
func TestMarshalStatusToolResponsePathsArePrefixes(t *testing.T) {
	status := &git.GitStatus{Branch: "main", Dirty: true, Files: []git.GitStatusEntry{
		{Path: "docs/a.md", Status: " M"},
		{Path: "docs-old/b.md", Status: " M"},
		{Path: "lycaon/internal/scan/x.go", Status: "M "},
		{Path: "lycaon/internal/scan/y.go", Status: "??"},
		{Path: "lycaon/internal/scanner.go", Status: " M"},
		{Path: "Taskfile.yml", Status: "MM"},
	}}
	obj := decodeStatusTool(t, status, git.StatusToolPage{Paths: []string{"docs", "lycaon/internal/scan/", "Taskfile.yml"}, Limit: 80})
	got := statusToolPaths(obj)
	want := []string{"docs/a.md", "lycaon/internal/scan/x.go", "lycaon/internal/scan/y.go", "Taskfile.yml"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("selected = %v want %v", got, want)
	}
	if obj["files_total"].(float64) != 4 {
		t.Fatalf("files_total = %v want 4", obj["files_total"])
	}
	if obj["staged_count"].(float64) != 2 || obj["unstaged_count"].(float64) != 3 || obj["untracked_count"].(float64) != 1 {
		t.Fatalf("counts = staged %v unstaged %v untracked %v", obj["staged_count"], obj["unstaged_count"], obj["untracked_count"])
	}
	echoed, _ := obj["paths"].([]any)
	if len(echoed) != 3 {
		t.Fatalf("paths echo = %v", obj["paths"])
	}
}

func TestMarshalStatusToolResponseDotPathSelectsWholeTree(t *testing.T) {
	obj := decodeStatusTool(t, statusToolFixture(3), git.StatusToolPage{Paths: []string{"."}, Limit: 80})
	if obj["files_total"].(float64) != 3 {
		t.Fatalf("files_total = %v want 3", obj["files_total"])
	}
}

func TestStatusSummaryOmitsFilesAndKeepsSelectionCounts(t *testing.T) {
	status := &git.GitStatus{Branch: "main", Dirty: true, Files: []git.GitStatusEntry{{Path: "src/a.go", Status: " M"}, {Path: "src/b.go", Status: "??"}, {Path: "docs/a.md", Status: " M"}}}
	got := git.BuildStatusToolResponse(status, git.StatusToolPage{Summary: true, Paths: []string{"src"}, Limit: 80})
	if !got.Summary || len(got.Files) != 0 || got.FilesTotal != 2 || got.UnstagedCount != 2 || got.UntrackedCount != 1 || len(got.Groups) != 1 || got.Groups[0].Files != 2 || got.NextOffset != nil {
		t.Fatalf("summary: %+v", got)
	}
}
