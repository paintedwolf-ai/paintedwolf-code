package survey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

// fakeTree stands in for the branch comparison, which needs a prepared worker
// tree. The wiring tests and workspacebaseline tests exercise the real one.
type fakeTree struct {
	changes []workspacebaseline.Change
	err     error
	// stall ignores the context until closed, like a filesystem call that hung.
	stall chan struct{}
	path  workspacebaseline.PathComparison
	calls int
	seen  tools.SourceTree
	// source answers TreePathChange for a move's source, by path.
	source map[string]workspacebaseline.PathComparison
	// unchanged lists the moves whose bytes kept the baseline's.
	unchanged map[workspacebaseline.PathMove]bool
}

func (f *fakeTree) TreeMovesUnchanged(context.Context, tools.SourceTree, []workspacebaseline.PathMove) (map[workspacebaseline.PathMove]bool, error) {
	return f.unchanged, f.err
}

func (f *fakeTree) TreeChanges(_ context.Context, tree tools.SourceTree) ([]workspacebaseline.Change, error) {
	f.calls++
	f.seen = tree
	if f.stall != nil {
		<-f.stall
	}
	return f.changes, f.err
}

func (f *fakeTree) TreePathChange(_ context.Context, tree tools.SourceTree, path string) (workspacebaseline.PathComparison, error) {
	f.calls++
	f.seen = tree
	if f.stall != nil {
		<-f.stall
	}
	if compared, ok := f.source[path]; ok {
		return compared, f.err
	}
	return f.path, f.err
}

// returnsDespiteStall runs a tool call against a stalled tree and fails if the
// call waits on it. Removing the budget reaches the failure here, not a test timeout.
func returnsDespiteStall(t *testing.T, stall chan struct{}, call func() string) string {
	t.Helper()
	done := make(chan string, 1)
	go func() { done <- call() }()
	defer close(stall)
	select {
	case out := <-done:
		return out
	case <-time.After(30 * time.Second):
		t.Fatal("the call waited on a stalled tree comparison")
		return ""
	}
}

func (h *historyHarness) workerWith(tree tools.SourceTreeReader) tools.ToolContext {
	worker := h.tctx
	worker.Identity.SessionID, worker.Identity.WorkerJobID = "child", "job-1"
	worker.Source.SourceWorkspaceKind = api.SourceWorkspaceKindWorker
	worker.Source.WorkerBaselinePath = "/baseline.db"
	worker.Source.History.Tree = tree
	return worker
}

func (h *historyHarness) recordOnWorker(seed effectSeed) {
	h.t.Helper()
	seed.branch, seed.session, seed.job = "job-1", "child", "job-1"
	h.record(seed)
}

func treeChanges(pairs ...string) []workspacebaseline.Change {
	out := make([]workspacebaseline.Change, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, workspacebaseline.Change{Path: pairs[i], Kind: workspacebaseline.ChangeKind(pairs[i+1])})
	}
	return out
}

func TestSourceHistoryOverviewACOV18TreeChangesBecomeUnrecordedRows(t *testing.T) {
	h := newHistoryHarness(t)
	h.recordOnWorker(effectSeed{path: "native.go", content: "native\n"})
	tree := &fakeTree{changes: treeChanges("@docs/guide.md", "modified", "gen.txt", "created", "obsolete.go", "deleted")}
	worker := h.workerWith(tree)
	worker.Source.WorkerBranchRoot = "/branch"
	result := runOverviewCall(t, h.tool, worker)

	if tree.seen.BaselinePath != "/baseline.db" || tree.seen.BranchRoot != "/branch" || len(tree.seen.Roots) != 2 {
		t.Fatalf("compared %+v, want the leg's baseline, branch and roots", tree.seen)
	}
	if result.Count != 4 || len(result.Files) != 4 || result.Files[0].Path != "native.go" || result.Files[0].Unrecorded {
		t.Fatalf("files = %+v, want the ledger row first and three more", result.Files)
	}
	wantOps := map[string]string{"@docs/guide.md": "write", "gen.txt": "create", "obsolete.go": "delete"}
	for _, row := range result.Files[1:] {
		if !row.Unrecorded || row.Op != wantOps[row.Path] || row.Actor != "" || row.At != "" || row.VersionID != "" || row.Effects != 0 {
			t.Fatalf("row %+v, want unrecorded %s and nothing the ledger would know", row, wantOps[row.Path])
		}
	}
	if result.Note != "" {
		t.Fatalf("note = %q, want none: the comparison finished", result.Note)
	}
}

func TestSourceHistoryOverviewACOV19ComparisonFailureListsLedgerRowsAndSaysSo(t *testing.T) {
	for name, tree := range map[string]tools.SourceTreeReader{
		"fails":           &fakeTree{err: errors.New("manifest unreadable")},
		"reader is unset": nil,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHistoryHarness(t)
			h.recordOnWorker(effectSeed{path: "native.go", content: "native\n"})
			result := runOverviewCall(t, h.tool, h.workerWith(tree))
			if !result.Recorded || len(result.Files) != 1 || result.Files[0].Path != "native.go" || result.Files[0].Unrecorded {
				t.Fatalf("files = %+v, want the ledger row alone", result.Files)
			}
			if !strings.Contains(result.Note, overviewTreeUnknownNote) {
				t.Fatalf("note = %q, want it to state the comparison did not finish", result.Note)
			}
		})
	}
}

func TestSourceHistoryOverviewACOV19StalledComparisonEndsAtTheBudget(t *testing.T) {
	h := newHistoryHarness(t)
	h.tool.treeBudget = time.Millisecond
	h.recordOnWorker(effectSeed{path: "native.go", content: "native\n"})
	tree := &fakeTree{stall: make(chan struct{}), changes: treeChanges("late.go", "created")}
	worker := h.workerWith(tree)
	var result overviewResult
	out := returnsDespiteStall(t, tree.stall, func() string {
		out, err := h.tool.Run(context.Background(), map[string]any{}, worker)
		if err != nil {
			t.Errorf("overview: %v", err)
		}
		return out
	})
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &result))
	if len(result.Files) != 1 || result.Files[0].Path != "native.go" || !strings.Contains(result.Note, overviewTreeUnknownNote) {
		t.Fatalf("result = %+v, want the ledger row and the note", result)
	}
}

func TestSourceHistoryDiffACOV19StalledComparisonEndsAtTheBudget(t *testing.T) {
	h := newHistoryHarness(t)
	h.tool.treeBudget = time.Millisecond
	h.writeFile("gen.txt", "on disk\n")
	tree := &fakeTree{stall: make(chan struct{})}
	worker := h.workerWith(tree)
	var result diffResult
	out := returnsDespiteStall(t, tree.stall, func() string {
		out, err := h.tool.Run(context.Background(), map[string]any{"path": "gen.txt", "mode": "diff"}, worker)
		if err != nil {
			t.Errorf("diff: %v", err)
		}
		return out
	})
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &result))
	if result.Unrecorded || result.Diff != "" || !strings.Contains(result.Note, overviewTreeUnknownNote) {
		t.Fatalf("result = %+v, want no diff and the note", result)
	}
}

func TestSourceHistoryOverviewACOV19EmptyComparisonKeepsTheEmptyListNote(t *testing.T) {
	h := newHistoryHarness(t)
	result := runOverviewCall(t, h.tool, h.workerWith(&fakeTree{}))
	if len(result.Files) != 0 || result.Note != overviewNoRowsNote {
		t.Fatalf("result = %+v, want no rows and the empty-list note", result)
	}
}

func TestSourceHistoryOverviewACOV20LedgerPathsAreNotDuplicated(t *testing.T) {
	h := newHistoryHarness(t)
	h.recordOnWorker(effectSeed{path: "edited.go", content: "edited\n"})
	h.recordOnWorker(effectSeed{path: "old.go", content: "moved\n"})
	h.recordOnWorker(effectSeed{path: "new.go", fromPath: "old.go", content: "moved\n", op: api.SourceChangeOpRename})
	h.recordOnWorker(effectSeed{root: "r2", path: "guide.md", content: "guide\n"})
	tree := &fakeTree{changes: treeChanges(
		"@docs/guide.md", "modified", "edited.go", "modified", "gen.txt", "created", "new.go", "created", "old.go", "deleted",
	)}
	result := runOverviewCall(t, h.tool, h.workerWith(tree))
	rows := map[string]bool{}
	for _, row := range result.Files {
		if _, seen := rows[row.Path]; seen {
			t.Fatalf("%s listed twice in %+v", row.Path, result.Files)
		}
		rows[row.Path] = row.Unrecorded
	}
	if unrecorded, ok := rows["gen.txt"]; !ok || !unrecorded {
		t.Fatalf("rows = %+v, want gen.txt unrecorded", rows)
	}
	if _, ok := rows["old.go"]; ok || len(rows) != 4 || rows["edited.go"] || rows["new.go"] || rows["@docs/guide.md"] {
		t.Fatalf("rows = %+v, want the ledger's rows kept and the rename's source covered", rows)
	}
}

func TestSourceHistoryOverviewACOV21MoveCoversOnlyUnchangedDescendants(t *testing.T) {
	h := newHistoryHarness(t)
	h.recordOnWorker(effectSeed{path: "b", fromPath: "a", kind: sourceledger.EntryKindDirectory, op: api.SourceChangeOpRename})
	h.recordOnWorker(effectSeed{path: "c", fromPath: "b", kind: sourceledger.EntryKindDirectory, op: api.SourceChangeOpRename})
	same := workspacebaseline.PathMove{From: "a/same.go", To: "c/same.go"}
	tree := &fakeTree{
		changes: treeChanges(
			"a/edited.go", "deleted", "a/same.go", "deleted", "c/edited.go", "created", "c/gen.txt", "created",
			"c/same.go", "created", "other/same.go", "created", "unrelated.go", "deleted",
		),
		unchanged: map[workspacebaseline.PathMove]bool{same: true},
	}
	result := runOverviewCall(t, h.tool, h.workerWith(tree))
	rows := map[string]bool{}
	for _, row := range result.Files {
		rows[row.Path] = row.Unrecorded
	}
	want := map[string]bool{"c": false, "a/edited.go": true, "c/edited.go": true, "c/gen.txt": true, "other/same.go": true, "unrelated.go": true}
	if fmt.Sprint(rows) != fmt.Sprint(want) {
		t.Fatalf("rows = %v, want %v", rows, want)
	}
}

func TestSourceHistoryOverviewACOV21FailedMoveCheckIsStated(t *testing.T) {
	h := newHistoryHarness(t)
	h.recordOnWorker(effectSeed{path: "b", fromPath: "a", kind: sourceledger.EntryKindDirectory, op: api.SourceChangeOpRename})
	worker := h.workerWith(failingMoves{&fakeTree{changes: treeChanges("a/x.go", "deleted", "b/x.go", "created")}})
	result := runOverviewCall(t, h.tool, worker)
	if !strings.Contains(result.Note, overviewTreeUnknownNote) {
		t.Fatalf("result = %+v, want the comparison stated as incomplete", result)
	}
}

type failingMoves struct{ *fakeTree }

func (failingMoves) TreeMovesUnchanged(context.Context, tools.SourceTree, []workspacebaseline.PathMove) (map[workspacebaseline.PathMove]bool, error) {
	return nil, errors.New("manifest unreadable")
}

func TestSourceHistoryOverviewACOV18ReusesTheRowCapAndTruncationNote(t *testing.T) {
	h := newHistoryHarness(t)
	var changes []workspacebaseline.Change
	for i := range overviewEffectCap + 1 {
		changes = append(changes, workspacebaseline.Change{Path: fmt.Sprintf("gen/%04d.txt", i), Kind: workspacebaseline.ChangeCreated})
	}
	result := runOverviewCall(t, h.tool, h.workerWith(&fakeTree{changes: changes}))
	if len(result.Files) != overviewEffectCap || result.Count != overviewEffectCap || !result.Truncated || !strings.Contains(result.Note, overviewTruncatedNote) {
		t.Fatalf("rows %d count %d truncated %v note %q, want the cap and its note", len(result.Files), result.Count, result.Truncated, result.Note)
	}
}

// A truncated page may omit a path's effects, so that path is checked in the
// ledger before it claims to have no row.
func TestSourceHistoryOverviewACOV20TruncatedPageDoesNotInventUnrecordedRows(t *testing.T) {
	h := newHistoryHarness(t)
	h.recordOnWorker(effectSeed{path: "oldest.go", content: "first\n"})
	h.recordOnWorker(effectSeed{root: "r2", path: "oldest.md", content: "first\n"})
	for i := range overviewEffectCap {
		h.recordOnWorker(effectSeed{path: "busy.go", content: fmt.Sprintf("v%d\n", i)})
	}
	tree := &fakeTree{changes: treeChanges("@docs/oldest.md", "modified", "@docs/unknown.md", "created", "gen.txt", "created", "oldest.go", "modified")}
	result := runOverviewCall(t, h.tool, h.workerWith(tree))
	rows := map[string]bool{}
	for _, row := range result.Files {
		rows[row.Path] = row.Unrecorded
	}
	if !result.Truncated || !rows["gen.txt"] || !rows["@docs/unknown.md"] || len(rows) != 3 {
		t.Fatalf("truncated %v rows %+v, want busy.go, gen.txt and @docs/unknown.md", result.Truncated, rows)
	}
	if _, listed := rows["oldest.go"]; listed {
		t.Fatalf("rows = %+v claim the ledger has nothing on oldest.go", rows)
	}
}

func TestSourceHistoryOverviewACOV18TrunkSessionIsUnchanged(t *testing.T) {
	h := newHistoryHarness(t)
	h.beginSession(historySession)
	tree := &fakeTree{changes: treeChanges("gen.txt", "created")}
	h.tctx.Source.History.Tree = tree
	result := runOverviewCall(t, h.tool, h.tctx)
	if tree.calls != 0 || len(result.Files) != 0 || result.Note != overviewNoRowsNote {
		t.Fatalf("calls %d result %+v, want no comparison on the trunk", tree.calls, result)
	}
	if out, err := h.tool.Run(t.Context(), map[string]any{"path": "main.go", "mode": "diff"}, h.tctx); err != nil || tree.calls != 0 {
		t.Fatalf("diff = %q, %v, calls %d, want no comparison on the trunk", out, err, tree.calls)
	}
}

func runTreeDiff(t *testing.T, h *historyHarness, tree tools.SourceTreeReader, path string) diffResult {
	t.Helper()
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(filepath.Join(h.dir, path)), 0o755))
	h.writeFile(path, "on disk\n")
	out, err := h.tool.Run(t.Context(), map[string]any{"path": path, "mode": "diff"}, h.workerWith(tree))
	testutil.FailErr(t, "tree diff", err)
	var result diffResult
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &result))
	return result
}

func TestSourceHistoryDiffACDF15UnrecordedFileComparesBaselineWithTheTree(t *testing.T) {
	for _, c := range []struct {
		name          string
		compared      workspacebaseline.PathComparison
		state         string
		removed, kept []string
	}{
		{
			name:     "modified",
			compared: workspacebaseline.PathComparison{Kind: workspacebaseline.ChangeModified, Before: "old\n", After: "new\n", HasBefore: true, HasAfter: true},
			removed:  []string{"old"}, kept: []string{"new"},
		},
		{
			name:     "created",
			compared: workspacebaseline.PathComparison{Kind: workspacebaseline.ChangeCreated, After: "new\n", HasBefore: true, HasAfter: true},
			kept:     []string{"new"},
		},
		{
			name:     "deleted",
			compared: workspacebaseline.PathComparison{Kind: workspacebaseline.ChangeDeleted, Before: "old\n", HasBefore: true, HasAfter: true},
			state:    "absent", removed: []string{"old"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHistoryHarness(t)
			result := runTreeDiff(t, h, &fakeTree{path: c.compared}, "gen.txt")
			if result.Recorded || !result.Unrecorded || result.VersionID != "" || result.BaseVersionID != "" || result.State != c.state || result.Since == nil {
				t.Fatalf("result = %+v, want an unrecorded diff with no versions and state %q", result, c.state)
			}
			removed, added := diffChanges(result.Diff)
			if fmt.Sprint(removed) != fmt.Sprint(c.removed) || fmt.Sprint(added) != fmt.Sprint(c.kept) {
				t.Fatalf("removed %q added %q in %q, want %q and %q", removed, added, result.Diff, c.removed, c.kept)
			}
		})
	}
}

func TestSourceHistoryDiffACDF15ANoTextSideSaysSoInsteadOfDiffing(t *testing.T) {
	h := newHistoryHarness(t)
	result := runTreeDiff(t, h, &fakeTree{path: workspacebaseline.PathComparison{Kind: workspacebaseline.ChangeModified, HasAfter: true, After: "new\n"}}, "big.bin")
	if !result.Unrecorded || result.Diff != "" || result.Note != treeDiffNoTextNote {
		t.Fatalf("result = %+v, want no diff and the no-text note", result)
	}
}

// An empty line diff says what is actually known about the bytes.
func TestSourceHistoryDiffACDF22EmptyDiffStatesWhatIsKnown(t *testing.T) {
	for _, c := range []struct {
		name          string
		kind          workspacebaseline.ChangeKind
		before, after string
		note          string
	}{
		{"final newline removed", workspacebaseline.ChangeModified, "a\n", "a", diffFinalNewlineNote},
		{"final newline added", workspacebaseline.ChangeModified, "a", "a\n", diffFinalNewlineNote},
		{"lone newline emptied", workspacebaseline.ChangeModified, "\n", "", diffFinalNewlineNote},
		{"timestamp only", workspacebaseline.ChangeModified, "a\n", "a\n", treeDiffTouchedNote},
		{"created empty", workspacebaseline.ChangeCreated, "", "", treeDiffCreatedEmptyNote},
		{"deleted empty", workspacebaseline.ChangeDeleted, "", "", treeDiffDeletedEmptyNote},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHistoryHarness(t)
			compared := workspacebaseline.PathComparison{Kind: c.kind, Before: c.before, After: c.after, HasBefore: true, HasAfter: true}
			result := runTreeDiff(t, h, &fakeTree{path: compared}, "f.txt")
			if result.Diff != "" || result.Note != c.note {
				t.Fatalf("result = %+v, want no diff and note %q", result, c.note)
			}
		})
	}
}

func TestSourceHistoryDiffACDF22FinalNewlineChangeInTheLedgerIsStated(t *testing.T) {
	h := newHistoryHarness(t)
	h.beginSession(historySession)
	h.record(effectSeed{path: "f.txt", content: "a", before: "a\n"})
	if result := h.sessionDiff("f.txt"); result.Diff != "" || result.Note != diffFinalNewlineNote {
		t.Fatalf("default diff = %+v, want the final-newline note", result)
	}
	result, err := h.diff(map[string]any{"path": "f.txt", "version_id": h.head("f.txt").VersionID})
	testutil.FailErr(t, "explicit diff", err)
	if result.Diff != "" || result.Note != diffFinalNewlineNote {
		t.Fatalf("explicit diff = %+v, want the final-newline note", result)
	}
}

func TestSourceHistoryDiffACDF15UnchangedPathKeepsTheLedgerAnswer(t *testing.T) {
	h := newHistoryHarness(t)
	result := runTreeDiff(t, h, &fakeTree{}, "untouched.go")
	if result.Unrecorded || result.Diff != "" || result.Note != sourceHistoryNoRecordNote {
		t.Fatalf("result = %+v, want the no-record note", result)
	}
}

// A generator rewrote the file after the agent's last recorded write, so the live
// side is the generator's and no version holds it.
func TestSourceHistoryDiffACDF16LiveFileWinsOverTheRecord(t *testing.T) {
	h := newHistoryHarness(t)
	h.recordOnWorker(effectSeed{path: "native.go", content: "native\n", before: "before\n"})
	tree := &fakeTree{path: workspacebaseline.PathComparison{Kind: workspacebaseline.ChangeModified, Before: "before\n", After: "generated\n", HasBefore: true, HasAfter: true}}
	result := runTreeDiff(t, h, tree, "native.go")
	if !result.Recorded || result.Unrecorded || result.VersionID != "" || result.BaseVersionID == "" || result.Note != treeDiffPastRecordNote {
		t.Fatalf("result = %+v, want the ledger's baseline, no live version and the note", result)
	}
	removed, added := diffChanges(result.Diff)
	if fmt.Sprint(removed) != "[before]" || fmt.Sprint(added) != "[generated]" {
		t.Fatalf("removed %q added %q in %q", removed, added, result.Diff)
	}
}

func TestSourceHistoryDiffACDF16LiveFileAtItsRecordNamesTheVersions(t *testing.T) {
	h := newHistoryHarness(t)
	h.recordOnWorker(effectSeed{path: "native.go", content: "native\n", before: "before\n"})
	tree := &fakeTree{path: workspacebaseline.PathComparison{Kind: workspacebaseline.ChangeModified, Before: "before\n", After: "native\n", HasBefore: true, HasAfter: true}}
	result := runTreeDiff(t, h, tree, "native.go")
	if !result.Recorded || result.Unrecorded || result.VersionID == "" || result.BaseVersionID == "" || result.Note != "" {
		t.Fatalf("result = %+v, want both versions named", result)
	}
}

func TestSourceHistoryDiffACDF16BaselineTheLedgerCannotVouchForNamesNoVersion(t *testing.T) {
	h := newHistoryHarness(t)
	h.recordOnWorker(effectSeed{path: "native.go", content: "native\n", before: "before\n"})
	tree := &fakeTree{path: workspacebaseline.PathComparison{Kind: workspacebaseline.ChangeModified, Before: "older\n", After: "native\n", HasBefore: true, HasAfter: true}}
	result := runTreeDiff(t, h, tree, "native.go")
	if result.BaseVersionID != "" || result.VersionID == "" {
		t.Fatalf("result = %+v, want no base version for bytes the ledger does not hold", result)
	}
}

func TestSourceHistoryDiffACDF16PathMatchingItsBaselineKeepsTheLedgerAnswer(t *testing.T) {
	h := newHistoryHarness(t)
	h.recordOnWorker(effectSeed{path: "native.go", content: "native\n", before: "before\n"})
	result := runTreeDiff(t, h, &fakeTree{}, "native.go")
	if !result.Recorded || result.Unrecorded || result.VersionID == "" || result.Diff == "" {
		t.Fatalf("result = %+v, want the ledger's own diff", result)
	}
}

// A file created natively and removed by a program is gone: its record is not
// the answer, and neither is the whole file as added.
func TestSourceHistoryDiffACDF20PathInNeitherBaselineNorTreeIsAbsent(t *testing.T) {
	h := newHistoryHarness(t)
	h.recordOnWorker(effectSeed{path: "tmp.txt", content: "scratch\n", op: api.SourceChangeOpCreate})
	result := runTreeDiff(t, h, &fakeTree{path: workspacebaseline.PathComparison{Absent: true}}, "tmp.txt")
	if !result.Recorded || result.Unrecorded || result.State != "absent" || result.Diff != "" || result.VersionID != "" || result.BaseVersionID != "" || result.Note != treeDiffAbsentNote {
		t.Fatalf("result = %+v, want a recorded absent answer with its note", result)
	}

	// With no record either, the honest answer is still that nothing is known.
	h = newHistoryHarness(t)
	result = runTreeDiff(t, h, &fakeTree{path: workspacebaseline.PathComparison{Absent: true}}, "never.txt")
	if result.Recorded || result.State != "" || result.Note != sourceHistoryNoRecordNote {
		t.Fatalf("result = %+v, want the no-record note", result)
	}
}

func created(after string) workspacebaseline.PathComparison {
	return workspacebaseline.PathComparison{Kind: workspacebaseline.ChangeCreated, After: after, HasBefore: true, HasAfter: true}
}

func TestSourceHistoryDiffACDF21FileMoveDiffsFromTheLedgersPredecessor(t *testing.T) {
	for _, c := range []struct {
		name          string
		live          string
		removed, kept []string
		note          string
	}{
		{name: "pure rename", live: "body\n", note: treeDiffMovedNote},
		{name: "rename then a program's edit", live: "changed\n", removed: []string{"body"}, kept: []string{"changed"}, note: treeDiffPastRecordNote},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHistoryHarness(t)
			h.record(effectSeed{path: "old.go", content: "body\n", session: "s0", op: api.SourceChangeOpCreate})
			h.recordOnWorker(effectSeed{path: "new.go", fromPath: "old.go", content: "body\n", op: api.SourceChangeOpRename})
			tree := &fakeTree{path: created(c.live), source: map[string]workspacebaseline.PathComparison{
				"old.go": {Kind: workspacebaseline.ChangeDeleted, Before: "body\n", HasBefore: true, HasAfter: true},
			}}
			result := runTreeDiff(t, h, tree, "new.go")
			removed, kept := diffChanges(result.Diff)
			if !result.Recorded || result.Unrecorded || result.BaseVersionID == "" || result.Note != c.note ||
				fmt.Sprint(removed) != fmt.Sprint(c.removed) || fmt.Sprint(kept) != fmt.Sprint(c.kept) {
				t.Fatalf("result = %+v, want recorded from its predecessor, removed %q, added %q, note %q", result, c.removed, c.kept, c.note)
			}
		})
	}
}

// The ledger captured these bytes at the move, after the program ran. The baseline
// at the source still holds what the leg began with.
func TestSourceHistoryDiffACDF21BaselineAtTheSourceOutranksTheLedger(t *testing.T) {
	for _, c := range []struct {
		name          string
		source        workspacebaseline.PathComparison
		removed, kept []string
		note          string
	}{
		{
			name:    "program edit then move",
			source:  workspacebaseline.PathComparison{Kind: workspacebaseline.ChangeDeleted, Before: "original\n", HasBefore: true, HasAfter: true},
			removed: []string{"original"}, kept: []string{"body"},
		},
		{name: "generated file moved", source: workspacebaseline.PathComparison{Absent: true}, kept: []string{"body"}},
		{name: "source the baseline holds no text for", source: workspacebaseline.PathComparison{Kind: workspacebaseline.ChangeDeleted, HasAfter: true}, note: treeDiffMovedNote},
		{name: "source reads as matching its baseline", source: workspacebaseline.PathComparison{}, note: treeDiffMovedNote},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHistoryHarness(t)
			h.record(effectSeed{path: "old.go", content: "body\n", session: "s0", op: api.SourceChangeOpCreate})
			h.recordOnWorker(effectSeed{path: "new.go", fromPath: "old.go", content: "body\n", op: api.SourceChangeOpRename})
			tree := &fakeTree{path: created("body\n"), source: map[string]workspacebaseline.PathComparison{"old.go": c.source}}
			result := runTreeDiff(t, h, tree, "new.go")
			removed, kept := diffChanges(result.Diff)
			if !result.Recorded || result.Unrecorded || result.Note != c.note || fmt.Sprint(removed) != fmt.Sprint(c.removed) || fmt.Sprint(kept) != fmt.Sprint(c.kept) {
				t.Fatalf("result = %+v, want removed %q, added %q, note %q", result, c.removed, c.kept, c.note)
			}
		})
	}
}

// A moved directory's descendant has no row of its own, so its bytes are the
// baseline's at the source.
func TestSourceHistoryDiffACDF21DescendantOfAMoveDiffsFromTheBaseline(t *testing.T) {
	deleted := workspacebaseline.PathComparison{Kind: workspacebaseline.ChangeDeleted, Before: "body\n", HasBefore: true, HasAfter: true}
	for _, c := range []struct {
		name          string
		source        workspacebaseline.PathComparison
		live          string
		removed, kept []string
		note          string
	}{
		{name: "unedited", source: deleted, live: "body\n", note: treeDiffMovedNote},
		{name: "edited by a program", source: deleted, live: "changed\n", removed: []string{"body"}, kept: []string{"changed"}},
		{name: "source the baseline never held", source: workspacebaseline.PathComparison{Absent: true}, live: "body\n", kept: []string{"body"}},
		{name: "source reads as matching its baseline", source: workspacebaseline.PathComparison{}, live: "body\n", note: treeDiffNoTextNote},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHistoryHarness(t)
			h.recordOnWorker(effectSeed{path: "b", fromPath: "a", kind: sourceledger.EntryKindDirectory, op: api.SourceChangeOpRename})
			testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(h.dir, "b"), 0o755))
			tree := &fakeTree{path: created(c.live), source: map[string]workspacebaseline.PathComparison{"a/x.go": c.source}}
			result := runTreeDiff(t, h, tree, "b/x.go")
			removed, kept := diffChanges(result.Diff)
			if !result.Recorded || result.Unrecorded || result.Note != c.note || fmt.Sprint(removed) != fmt.Sprint(c.removed) || fmt.Sprint(kept) != fmt.Sprint(c.kept) {
				t.Fatalf("result = %+v, want recorded, removed %q, added %q, note %q", result, c.removed, c.kept, c.note)
			}
		})
	}
}

func TestSourceHistoryDiffACDF21OldNameOfAMoveIsAbsentAndRecorded(t *testing.T) {
	h := newHistoryHarness(t)
	h.recordOnWorker(effectSeed{path: "dir/old.go", content: "body\n", op: api.SourceChangeOpCreate})
	h.recordOnWorker(effectSeed{path: "dir/new.go", fromPath: "dir/old.go", op: api.SourceChangeOpRename})
	gone := workspacebaseline.PathComparison{Kind: workspacebaseline.ChangeDeleted, Before: "body\n", HasBefore: true, HasAfter: true}
	result := runTreeDiff(t, h, &fakeTree{path: gone}, "dir/old.go")
	if !result.Recorded || result.Unrecorded || result.State != "absent" {
		t.Fatalf("result = %+v, want recorded and absent", result)
	}
	// A deletion the ledger holds nothing about stays unrecorded.
	result = runTreeDiff(t, h, &fakeTree{path: gone}, "dir/other.go")
	if result.Recorded || !result.Unrecorded || result.State != "absent" {
		t.Fatalf("result = %+v, want unrecorded and absent", result)
	}
}

func TestSourceHistoryDiffACDF21FollowsAChainOfDirectoryMoves(t *testing.T) {
	h := newHistoryHarness(t)
	h.recordOnWorker(effectSeed{path: "b", fromPath: "a", kind: sourceledger.EntryKindDirectory, op: api.SourceChangeOpRename})
	h.recordOnWorker(effectSeed{path: "c", fromPath: "b", kind: sourceledger.EntryKindDirectory, op: api.SourceChangeOpRename})
	tree := &fakeTree{path: created("body\n"), source: map[string]workspacebaseline.PathComparison{
		"a/sub/x.go": {Kind: workspacebaseline.ChangeDeleted, Before: "base\n", HasBefore: true, HasAfter: true},
	}}
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(h.dir, "c", "sub"), 0o755))
	result := runTreeDiff(t, h, tree, "c/sub/x.go")
	removed, added := diffChanges(result.Diff)
	if !result.Recorded || fmt.Sprint(removed) != "[base]" || fmt.Sprint(added) != "[body]" {
		t.Fatalf("result = %+v, want the first location's baseline against the live file", result)
	}
}

// A move's base text keeps its origin: its exposure is recorded at the old name.
func TestSourceHistoryACDF19MovedCredentialFileOnAWriteLeg(t *testing.T) {
	h := newHistoryHarness(t)
	h.record(effectSeed{path: ".env", content: "TOKEN=old\n", session: "s0", op: api.SourceChangeOpCreate})
	h.recordOnWorker(effectSeed{path: "settings.txt", fromPath: ".env", content: "TOKEN=old\n", op: api.SourceChangeOpRename})
	h.writeFile("settings.txt", "TOKEN=new\n")
	worker := h.workerWith(&fakeTree{path: created("TOKEN=new\n"), source: map[string]workspacebaseline.PathComparison{
		".env": {Kind: workspacebaseline.ChangeDeleted, Before: "TOKEN=old\n", HasBefore: true, HasAfter: true},
	}})
	recorder := &deliveredRecorder{}
	worker.Effects.CredentialFiles = recorder
	_, err := h.tool.Run(t.Context(), map[string]any{"path": "settings.txt", "mode": "diff"}, worker)
	testutil.FailErr(t, "diff", err)
	if len(recorder.reads) != 1 || recorder.reads[0].Path != ".env" || recorder.reads[0].Content != "TOKEN=old\n" {
		t.Fatalf("delivered %+v, want the old name's text", recorder.reads)
	}
}

// Lost bytes say nothing about whether the live file moved past its record.
func TestSourceHistoryDiffACDF23LostRecordIsNotCalledAChange(t *testing.T) {
	h := newHistoryHarness(t)
	h.recordOnWorker(effectSeed{path: "native.go", content: "native\n", before: "before\n"})
	testutil.FailErr(t, "drop stored content", os.RemoveAll(h.blobs))
	tree := &fakeTree{path: workspacebaseline.PathComparison{Kind: workspacebaseline.ChangeModified, Before: "before\n", After: "native\n", HasBefore: true, HasAfter: true}}
	result := runTreeDiff(t, h, tree, "native.go")
	if result.VersionID != "" || result.Note != treeDiffRecordUnavailableNote || result.Diff == "" {
		t.Fatalf("result = %+v, want the diff and the unavailable note", result)
	}
}

// The ledger rejecting its own record does not stop the tree from answering, and
// still reaches the caller when the tree has nothing to say.
func TestSourceHistoryDiffACDF16LedgerRejectionYieldsToTheTree(t *testing.T) {
	seed := func(h *historyHarness) {
		h.recordOnWorker(effectSeed{path: "native.go", content: "native\n", before: "before\n"})
		testutil.FailErr(t, "drop stored content", os.RemoveAll(h.blobs))
	}
	h := newHistoryHarness(t)
	seed(h)
	tree := &fakeTree{path: workspacebaseline.PathComparison{Kind: workspacebaseline.ChangeModified, Before: "before\n", After: "generated\n", HasBefore: true, HasAfter: true}}
	result := runTreeDiff(t, h, tree, "native.go")
	if !result.Recorded || result.VersionID != "" || result.BaseVersionID != "" || result.Note != treeDiffRecordUnavailableNote || result.Diff == "" {
		t.Fatalf("result = %+v, want the tree's diff with no versions", result)
	}

	h = newHistoryHarness(t)
	seed(h)
	h.writeFile("native.go", "on disk\n")
	if out, err := h.tool.Run(t.Context(), map[string]any{"path": "native.go", "mode": "diff"}, h.workerWith(&fakeTree{})); err == nil {
		t.Fatalf("diff = %q, want the ledger's rejection when the tree has nothing to say", out)
	}
}

func TestSourceHistoryDiffACOV19ComparisonFailureAddsANote(t *testing.T) {
	for name, tree := range map[string]tools.SourceTreeReader{
		"fails":           &fakeTree{err: errors.New("manifest unreadable")},
		"reader is unset": nil,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHistoryHarness(t)
			result := runTreeDiff(t, h, tree, "gen.txt")
			if result.Unrecorded || result.Diff != "" || !strings.Contains(result.Note, overviewTreeUnknownNote) || !strings.Contains(result.Note, sourceHistoryNoRecordNote) {
				t.Fatalf("result = %+v, want both notes and no diff", result)
			}
		})
	}
}

type failingPathLookup struct{ tools.SourceFileHistory }

func (failingPathLookup) OldestPathEffectAfterOnBranch(context.Context, string, sourcebranch.ID, string, string, int64) (sourceledger.FileEffectBounds, bool, error) {
	return sourceledger.FileEffectBounds{}, false, errors.New("ledger unavailable")
}

func TestSourceHistoryOverviewACOV19LedgerLookupFailureIsStated(t *testing.T) {
	h := newHistoryHarness(t)
	for i := range overviewEffectCap + 1 {
		h.recordOnWorker(effectSeed{path: "busy.go", content: fmt.Sprintf("v%d\n", i)})
	}
	worker := h.workerWith(&fakeTree{changes: treeChanges("gen.txt", "created")})
	worker.Source.History.Files = failingPathLookup{worker.Source.History.Files}
	result := runOverviewCall(t, h.tool, worker)
	if len(result.Files) != 1 || !strings.Contains(result.Note, overviewTreeUnknownNote) {
		t.Fatalf("result = %+v, want the ledger row and the note", result)
	}
}

func manyLines(prefix string, count int) string {
	var text strings.Builder
	for i := range count {
		fmt.Fprintf(&text, "%s line %06d of generated source text\n", prefix, i)
	}
	return text.String()
}

// allocatedBy reports what call allocated in total, which bounds its peak.
func allocatedBy(call func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	call()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// Unbounded, a rewrite of this size allocates gigabytes: the edit search is
// quadratic in the edit depth.
const largeDiffAllocationLimit = 2 * 4 * sourceHistoryDiffMaxCells

func TestSourceHistoryDiffACDF18LargeRewriteAnswersWithANote(t *testing.T) {
	const lines = 20_000
	before, after := manyLines("old", lines), manyLines("new", lines)

	t.Run("live file on a write leg", func(t *testing.T) {
		h := newHistoryHarness(t)
		tree := &fakeTree{path: workspacebaseline.PathComparison{Kind: workspacebaseline.ChangeModified, Before: before, After: after, HasBefore: true, HasAfter: true}}
		var result diffResult
		if allocated := allocatedBy(func() { result = runTreeDiff(t, h, tree, "gen.txt") }); allocated > largeDiffAllocationLimit {
			t.Fatalf("allocated %d bytes, limit %d", allocated, int64(largeDiffAllocationLimit))
		}
		if !result.Unrecorded || result.Diff != "" || result.Note != diffTooLargeNote(len(before), len(after), "", "") {
			t.Fatalf("diff %d bytes, note %q, want no diff and the sizes", len(result.Diff), result.Note)
		}
	})
	t.Run("default diff from the ledger", func(t *testing.T) {
		h := newHistoryHarness(t)
		h.beginSession(historySession)
		h.record(effectSeed{path: "gen.txt", content: after, before: before})
		var result diffResult
		if allocated := allocatedBy(func() { result = h.sessionDiff("gen.txt") }); allocated > largeDiffAllocationLimit {
			t.Fatalf("allocated %d bytes, limit %d", allocated, int64(largeDiffAllocationLimit))
		}
		if !result.Recorded || result.Diff != "" || result.BaseVersionID == "" || result.VersionID == "" ||
			result.Note != diffTooLargeNote(len(before), len(after), result.BaseVersionID, result.VersionID) {
			t.Fatalf("diff %d bytes, result note %q versions %q %q, want no diff and both sizes and versions", len(result.Diff), result.Note, result.BaseVersionID, result.VersionID)
		}
	})
	t.Run("explicit version", func(t *testing.T) {
		h := newHistoryHarness(t)
		h.record(effectSeed{path: "gen.txt", content: after, before: before})
		version := h.head("gen.txt").VersionID
		var result diffResult
		var err error
		if allocated := allocatedBy(func() { result, err = h.diff(map[string]any{"path": "gen.txt", "version_id": version}) }); allocated > largeDiffAllocationLimit {
			t.Fatalf("allocated %d bytes, limit %d", allocated, int64(largeDiffAllocationLimit))
		}
		testutil.FailErr(t, "explicit diff", err)
		if result.Diff != "" || result.Note != diffTooLargeNote(len(before), len(after), result.BaseVersionID, version) {
			t.Fatalf("diff %d bytes, note %q, want no diff and the note", len(result.Diff), result.Note)
		}
	})
}

// The ceiling admits a whole rewrite of an ordinary source file, and a file that
// only grew costs no edit search however large it is.
func TestSourceHistoryDiffACDF18CeilingAdmitsOrdinaryFiles(t *testing.T) {
	for name, compared := range map[string]workspacebaseline.PathComparison{
		"rewrite of 1,500 lines":    {Kind: workspacebaseline.ChangeModified, Before: manyLines("old", 1_500), After: manyLines("new", 1_500), HasBefore: true, HasAfter: true},
		"new file of 50,000 lines":  {Kind: workspacebaseline.ChangeCreated, After: manyLines("new", 50_000), HasBefore: true, HasAfter: true},
		"gone file of 50,000 lines": {Kind: workspacebaseline.ChangeDeleted, Before: manyLines("old", 50_000), HasBefore: true, HasAfter: true},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHistoryHarness(t)
			var result diffResult
			if allocated := allocatedBy(func() { result = runTreeDiff(t, h, &fakeTree{path: compared}, "gen.txt") }); allocated > largeDiffAllocationLimit {
				t.Fatalf("allocated %d bytes, limit %d", allocated, int64(largeDiffAllocationLimit))
			}
			if result.Diff == "" || result.Note != "" {
				t.Fatalf("diff %d bytes, note %q, want the diff", len(result.Diff), result.Note)
			}
		})
	}
}
