package survey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	blocks  bool
	path    workspacebaseline.PathComparison
	calls   int
	seen    tools.SourceTree
}

func (f *fakeTree) TreeChanges(ctx context.Context, tree tools.SourceTree) ([]workspacebaseline.Change, error) {
	f.calls++
	f.seen = tree
	if f.blocks {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.changes, f.err
}

func (f *fakeTree) TreePathChange(_ context.Context, tree tools.SourceTree, _ string) (workspacebaseline.PathComparison, error) {
	f.calls++
	f.seen = tree
	return f.path, f.err
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
		"exceeds budget":  &fakeTree{blocks: true},
		"reader is unset": nil,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHistoryHarness(t)
			h.tool.treeBudget = time.Nanosecond
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

func TestSourceHistoryDiffACDF15ATouchedFileSaysItsTextIsUnchanged(t *testing.T) {
	h := newHistoryHarness(t)
	result := runTreeDiff(t, h, &fakeTree{path: workspacebaseline.PathComparison{Kind: workspacebaseline.ChangeModified, Before: "same\n", After: "same\n", HasBefore: true, HasAfter: true}}, "touched.go")
	if !result.Unrecorded || result.Diff != "" || result.Note != treeDiffTouchedNote {
		t.Fatalf("result = %+v, want an empty diff and the touched note", result)
	}
}

func TestSourceHistoryDiffACDF15UnchangedPathKeepsTheLedgerAnswer(t *testing.T) {
	h := newHistoryHarness(t)
	result := runTreeDiff(t, h, &fakeTree{}, "untouched.go")
	if result.Unrecorded || result.Diff != "" || result.Note != sourceHistoryNoRecordNote {
		t.Fatalf("result = %+v, want the no-record note", result)
	}
}

func TestSourceHistoryDiffACDF15PathWithAnEffectKeepsTheLedgerAnswer(t *testing.T) {
	h := newHistoryHarness(t)
	h.recordOnWorker(effectSeed{path: "native.go", content: "native\n", before: "before\n"})
	tree := &fakeTree{path: workspacebaseline.PathComparison{Kind: workspacebaseline.ChangeModified, HasBefore: true, HasAfter: true}}
	result := runTreeDiff(t, h, tree, "native.go")
	if result.Unrecorded || !result.Recorded || tree.calls != 0 {
		t.Fatalf("result = %+v after %d comparisons, want the ledger's own diff", result, tree.calls)
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

// A credential file's text reaches the model only with its exposure recorded,
// the same as a read.
func TestSourceHistoryDiffACDF15CredentialFileRecordsItsDelivery(t *testing.T) {
	h := newHistoryHarness(t)
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(h.dir, ".env"), []byte("TOKEN=new\n"), 0o600))
	recorder := &deliveredRecorder{}
	worker := h.workerWith(&fakeTree{path: workspacebaseline.PathComparison{
		Kind: workspacebaseline.ChangeModified, Abs: filepath.Join(h.dir, ".env"),
		Before: "TOKEN=old\n", After: "TOKEN=new\n", HasBefore: true, HasAfter: true,
	}})
	worker.Effects.CredentialFiles = recorder
	_, err := h.tool.Run(t.Context(), map[string]any{"path": ".env", "mode": "diff"}, worker)
	testutil.FailErr(t, "diff credential file", err)
	if len(recorder.reads) != 2 || recorder.reads[0].Content != "TOKEN=old\n" || recorder.reads[1].Content != "TOKEN=new\n" {
		t.Fatalf("delivered = %+v, want both sides", recorder.reads)
	}

	recorder.err = errors.New("evidence store down")
	if out, err := h.tool.Run(t.Context(), map[string]any{"path": ".env", "mode": "diff"}, worker); err == nil {
		t.Fatalf("diff = %q, want it withheld when exposure cannot be recorded", out)
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

func TestDisplayPathLocationNeedsAPrimaryRoot(t *testing.T) {
	if _, _, err := displayPathLocation(nil, "gen.txt"); err == nil {
		t.Fatal("a path resolved with no roots")
	}
}
