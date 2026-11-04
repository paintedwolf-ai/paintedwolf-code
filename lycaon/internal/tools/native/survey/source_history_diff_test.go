package survey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type diffResult struct {
	Mode          string `json:"mode"`
	Recorded      bool   `json:"recorded"`
	BaseVersionID string `json:"base_version_id"`
	VersionID     string `json:"version_id"`
	Diff          string `json:"diff"`
	Note          string `json:"note"`
	State         string `json:"state"`
	Since         *struct {
		Ordinal int64 `json:"ordinal"`
	} `json:"since"`
}

func (h *historyHarness) diff(args map[string]any) (diffResult, error) {
	h.t.Helper()
	call := map[string]any{"mode": "diff"}
	for key, value := range args {
		call[key] = value
	}
	out, err := h.tool.Run(h.t.Context(), call, h.tctx)
	if err != nil {
		return diffResult{}, err
	}
	var result diffResult
	testutil.FailErr(h.t, "decode diff", json.Unmarshal([]byte(out), &result))
	return result, nil
}

func (h *historyHarness) sessionDiff(path string) diffResult {
	h.t.Helper()
	h.writeFile(path, "on disk\n")
	result, err := h.diff(map[string]any{"path": path})
	testutil.FailErr(h.t, "session diff", err)
	if result.Mode != "diff" {
		h.t.Fatalf("mode = %q", result.Mode)
	}
	return result
}

func diffChanges(diff string) (removed, added []string) {
	for line := range strings.SplitSeq(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
		case strings.HasPrefix(line, "-"):
			removed = append(removed, strings.TrimPrefix(line, "- "))
		case strings.HasPrefix(line, "+"):
			added = append(added, strings.TrimPrefix(line, "+ "))
		}
	}
	return removed, added
}

func expectSingleChange(t *testing.T, diff, removed, added string) {
	t.Helper()
	gotRemoved, gotAdded := diffChanges(diff)
	if len(gotRemoved) != 1 || gotRemoved[0] != removed || len(gotAdded) != 1 || gotAdded[0] != added {
		t.Fatalf("diff = %q, want %q replaced by %q", diff, removed, added)
	}
}

func TestSourceHistoryDiffACDF01ComparesHeadWithTheSessionBaseline(t *testing.T) {
	h := newHistoryHarness(t)
	h.record(effectSeed{path: "a.go", content: "before\n", session: "s0", op: api.SourceChangeOpCreate})
	baseline := h.head("a.go").VersionID
	h.beginSession(historySession)
	h.record(effectSeed{path: "a.go", content: "middle\n"})
	h.record(effectSeed{path: "a.go", content: "after\n"})

	result := h.sessionDiff("a.go")
	head := h.head("a.go").VersionID
	if !result.Recorded || result.BaseVersionID != baseline || result.VersionID != head || result.Since == nil {
		t.Fatalf("diff = %+v, want %s to %s with since", result, baseline, head)
	}
	expectSingleChange(t, result.Diff, "before", "after")
}

func TestSourceHistoryDiffACDF02CreatedThisSessionDiffsFromAbsence(t *testing.T) {
	h := newHistoryHarness(t)
	h.beginSession(historySession)
	h.record(effectSeed{path: "new.go", content: "fresh\n", op: api.SourceChangeOpCreate})

	result := h.sessionDiff("new.go")
	removed, added := diffChanges(result.Diff)
	if result.BaseVersionID != "" || len(removed) != 0 || len(added) != 1 || added[0] != "fresh" {
		t.Fatalf("created diff = %+v, want all lines added from absence", result)
	}
	if !strings.Contains(result.Diff, "(absent)") {
		t.Fatalf("diff = %q, want the absent label", result.Diff)
	}
}

func TestSourceHistoryDiffACDF03UntouchedThisSessionIsAnEmptyDiff(t *testing.T) {
	h := newHistoryHarness(t)
	h.record(effectSeed{path: "a.go", content: "user change\n", origin: api.SourceChangeOriginUser, op: api.SourceChangeOpCreate})
	h.beginSession(historySession)

	result := h.sessionDiff("a.go")
	head := h.head("a.go").VersionID
	if !result.Recorded || result.Diff != "" || result.BaseVersionID != head || result.VersionID != head || result.Note == "" || result.Since == nil {
		t.Fatalf("untouched diff = %+v, want an empty recorded diff at head with a note", result)
	}
}

func TestSourceHistoryDiffACDF04WorkerEditsStayOutOfTheTrunkDiff(t *testing.T) {
	h := newHistoryHarness(t)
	h.record(effectSeed{path: "a.go", content: "before\n", session: "s0", op: api.SourceChangeOpCreate})
	baseline := h.head("a.go").VersionID
	h.beginSession(historySession)
	h.record(effectSeed{path: "a.go", content: "worker\n", branch: "job-1", session: "child", job: "job-1"})

	workerOnly := h.sessionDiff("a.go")
	if workerOnly.Diff != "" || workerOnly.BaseVersionID != baseline || workerOnly.Note == "" {
		t.Fatalf("worker-only diff = %+v, want no trunk effect", workerOnly)
	}

	h.record(effectSeed{path: "a.go", content: "trunk\n"})
	result := h.sessionDiff("a.go")
	if result.BaseVersionID != baseline {
		t.Fatalf("base = %s, want the trunk baseline %s", result.BaseVersionID, baseline)
	}
	expectSingleChange(t, result.Diff, "before", "trunk")
}

func TestSourceHistoryDiffACDF05ExplicitVersionIsUnchanged(t *testing.T) {
	h := newHistoryHarness(t)
	h.record(effectSeed{path: "a.go", content: "one\n", session: "s0", op: api.SourceChangeOpCreate})
	first := h.head("a.go").VersionID
	h.beginSession(historySession)
	h.record(effectSeed{path: "a.go", content: "two\n"})
	second := h.head("a.go").VersionID
	h.record(effectSeed{path: "a.go", content: "three\n"})
	third := h.head("a.go").VersionID
	h.writeFile("a.go", "three\n")

	parent, err := h.diff(map[string]any{"path": "a.go", "version_id": third})
	testutil.FailErr(t, "diff against parent", err)
	if parent.BaseVersionID != second || parent.Since != nil {
		t.Fatalf("parent diff = %+v, want base %s and no since", parent, second)
	}
	expectSingleChange(t, parent.Diff, "two", "three")

	pair, err := h.diff(map[string]any{"path": "a.go", "version_id": third, "base_version_id": first})
	testutil.FailErr(t, "diff pair", err)
	expectSingleChange(t, pair.Diff, "one", "three")

	head, err := h.diff(map[string]any{"path": "a.go", "version_id": first, "base_version_id": "head"})
	testutil.FailErr(t, "diff against head", err)
	expectSingleChange(t, head.Diff, "three", "one")
}

func expectReject(t *testing.T, err error, code string) *toolrejection.ToolReject {
	t.Helper()
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
	return reject
}

func TestSourceHistoryDiffACDF06BaseAloneNeedsAVersion(t *testing.T) {
	h := newHistoryHarness(t)
	h.beginSession(historySession)
	h.writeFile("a.go", "x\n")
	for _, base := range []string{"v1", "head"} {
		_, err := h.diff(map[string]any{"path": "a.go", "base_version_id": base})
		if reject := expectReject(t, err, "SOURCE_VERSION_REQUIRED"); reject.Data["mode"] != "diff" {
			t.Fatalf("reject mode = %v, want diff", reject.Data["mode"])
		}
	}
}

func TestSourceHistoryDiffACDF07VersionModeStillNeedsAVersion(t *testing.T) {
	h := newHistoryHarness(t)
	h.writeFile("a.go", "x\n")
	_, err := h.tool.Run(t.Context(), map[string]any{"path": "a.go", "mode": "version"}, h.tctx)
	if reject := expectReject(t, err, "SOURCE_VERSION_REQUIRED"); reject.Data["mode"] != "version" {
		t.Fatalf("reject mode = %v, want version", reject.Data["mode"])
	}
}

// fixedComparison answers every comparison with one prepared pair.
type fixedComparison struct {
	comparison sourceledger.Comparison
	err        error
}

func (f fixedComparison) CompareVersions(context.Context, string, string) (sourceledger.Comparison, error) {
	return f.comparison, f.err
}

func (f fixedComparison) CompareVersionPair(context.Context, string, string, string) (sourceledger.Comparison, error) {
	return f.comparison, f.err
}

func TestSourceHistoryDiffACDF08RejectsSidesWithNoText(t *testing.T) {
	states := []sourceledger.ContentAvailability{
		sourceledger.ContentUnavailable, sourceledger.ContentNotCaptured, sourceledger.ContentBinary, sourceledger.ContentUnresolved,
	}
	calls := map[string]map[string]any{
		"default":  {"path": "a.go"},
		"explicit": {"path": "a.go", "version_id": "v-head"},
		"pair":     {"path": "a.go", "version_id": "v-head", "base_version_id": "v-base"},
	}
	for callName, call := range calls {
		for _, state := range states {
			for side, id := range map[string]string{"base": "v-base", "version": "v-head"} {
				t.Run(fmt.Sprintf("%s %s %s", callName, state, side), func(t *testing.T) {
					h := newHistoryHarness(t)
					h.beginSession(historySession)
					h.record(effectSeed{path: "a.go", content: "text\n", op: api.SourceChangeOpCreate})
					h.writeFile("a.go", "text\n")
					comparison := sourceledger.Comparison{
						Before: sourceledger.ComparisonSide{VersionID: "v-base", Path: "a.go", Availability: sourceledger.ContentAvailable, Content: "old\n"},
						After:  sourceledger.ComparisonSide{VersionID: "v-head", Path: "a.go", Availability: sourceledger.ContentAvailable, Content: "new\n"},
					}
					if side == "base" {
						comparison.Before.Availability, comparison.Before.Content = state, ""
					} else {
						comparison.After.Availability, comparison.After.Content = state, ""
					}
					h.tctx.Source.History.Comparison = fixedComparison{comparison: comparison}
					_, err := h.diff(call)
					reject := expectReject(t, err, "SOURCE_VERSION_UNAVAILABLE")
					if reject.Data["version_id"] != id || reject.Data["side"] != side || reject.Data["state"] != string(state) {
						t.Fatalf("reject data = %v, want %s side %s in state %s", reject.Data, side, id, state)
					}
				})
			}
		}
	}
}

// The recorded bytes reach the rejection through the real comparison path.
func TestSourceHistoryDiffACDF08RecordedBinaryRejectsOnBothPaths(t *testing.T) {
	h := newHistoryHarness(t)
	h.record(effectSeed{path: "a.bin", content: "\x00\xff binary", session: "s0", op: api.SourceChangeOpCreate})
	binary := h.head("a.bin").VersionID
	h.beginSession(historySession)
	h.record(effectSeed{path: "a.bin", content: "text\n"})
	head := h.head("a.bin").VersionID
	h.writeFile("a.bin", "text\n")

	_, err := h.diff(map[string]any{"path": "a.bin"})
	reject := expectReject(t, err, "SOURCE_VERSION_UNAVAILABLE")
	if reject.Data["version_id"] != binary || reject.Data["side"] != "base" || reject.Data["state"] != string(sourceledger.ContentBinary) {
		t.Fatalf("default path reject = %v", reject.Data)
	}
	_, err = h.diff(map[string]any{"path": "a.bin", "version_id": head})
	reject = expectReject(t, err, "SOURCE_VERSION_UNAVAILABLE")
	if reject.Data["version_id"] != binary || reject.Data["side"] != "base" {
		t.Fatalf("explicit path reject = %v", reject.Data)
	}
}

func TestSourceHistoryDiffACDF09UnknownWindowIsStated(t *testing.T) {
	h := newHistoryHarness(t)
	h.record(effectSeed{path: "a.go", content: "x\n", session: "s0", op: api.SourceChangeOpCreate})
	result := h.sessionDiff("a.go")
	if result.Recorded || result.Note == "" || result.Diff != "" || result.Since != nil {
		t.Fatalf("unknown window diff = %+v, want recorded=false with a note", result)
	}
}

func TestSourceHistoryDiffACDF10BaselineIsNotLimitedToOnePage(t *testing.T) {
	h := newHistoryHarness(t)
	h.record(effectSeed{path: "a.go", content: "before\n", session: "s0", op: api.SourceChangeOpCreate})
	baseline := h.head("a.go").VersionID
	h.beginSession(historySession)
	for i := range 150 {
		h.record(effectSeed{path: "a.go", content: fmt.Sprintf("edit %d\n", i)})
	}
	result := h.sessionDiff("a.go")
	if result.BaseVersionID != baseline {
		t.Fatalf("base = %s, want the version before the first of 150 edits (%s)", result.BaseVersionID, baseline)
	}
	expectSingleChange(t, result.Diff, "before", "edit 149")
}

// Both versions come from the file's own record, so a move in the window is not
// a mismatch between the path and its baseline.
func TestSourceHistoryDiffFollowsAMoveInTheWindow(t *testing.T) {
	h := newHistoryHarness(t)
	h.record(effectSeed{path: "old.go", content: "body\n", session: "s0", op: api.SourceChangeOpCreate})
	baseline := h.head("old.go").VersionID
	h.beginSession(historySession)
	h.record(effectSeed{path: "new.go", fromPath: "old.go", op: api.SourceChangeOpRename})
	h.record(effectSeed{path: "new.go", content: "body changed\n"})

	result := h.sessionDiff("new.go")
	if result.BaseVersionID != baseline {
		t.Fatalf("moved diff = %+v, want base %s", result, baseline)
	}
	expectSingleChange(t, result.Diff, "body", "body changed")
}

func TestSourceHistoryDiffACDF11DeletedThisSessionDiffsToAbsence(t *testing.T) {
	deletedDiff := func(t *testing.T, h *historyHarness, path string) diffResult {
		t.Helper()
		result, err := h.diff(map[string]any{"path": path})
		testutil.FailErr(t, "diff deleted path", err)
		return result
	}
	t.Run("baseline to absence", func(t *testing.T) {
		h := newHistoryHarness(t)
		h.record(effectSeed{path: "a.go", content: "before\n", session: "s0", op: api.SourceChangeOpCreate})
		baseline := h.head("a.go").VersionID
		h.beginSession(historySession)
		h.record(effectSeed{path: "a.go", content: "edited\n"})
		h.remove("a.go", sourcebranch.Trunk)

		result := deletedDiff(t, h, "a.go")
		removed, added := diffChanges(result.Diff)
		if !result.Recorded || result.BaseVersionID != baseline || result.VersionID != "" || result.State != "absent" || result.Since == nil {
			t.Fatalf("deleted diff = %+v, want base %s and an absent end", result, baseline)
		}
		if len(removed) != 1 || removed[0] != "before" || len(added) != 0 {
			t.Fatalf("diff = %q, want the baseline removed", result.Diff)
		}
	})
	t.Run("created and deleted this session is empty", func(t *testing.T) {
		h := newHistoryHarness(t)
		h.beginSession(historySession)
		h.record(effectSeed{path: "a.go", content: "brief\n", op: api.SourceChangeOpCreate})
		h.remove("a.go", sourcebranch.Trunk)
		result := deletedDiff(t, h, "a.go")
		if !result.Recorded || result.Diff != "" || result.BaseVersionID != "" || result.State != "absent" {
			t.Fatalf("diff = %+v, want an empty diff between two absences", result)
		}
	})
	t.Run("deleted before the session has no effect in the window", func(t *testing.T) {
		h := newHistoryHarness(t)
		h.record(effectSeed{path: "a.go", content: "old\n", session: "s0", op: api.SourceChangeOpCreate})
		h.remove("a.go", sourcebranch.Trunk)
		h.beginSession(historySession)
		result := deletedDiff(t, h, "a.go")
		if !result.Recorded || result.Diff != "" || result.Note == "" || result.State != "absent" {
			t.Fatalf("diff = %+v, want an empty diff with a note", result)
		}
	})
	t.Run("the caller's branch decides", func(t *testing.T) {
		h := newHistoryHarness(t)
		h.record(effectSeed{path: "a.go", content: "before\n", session: "s0", op: api.SourceChangeOpCreate})
		h.beginSession(historySession)
		h.record(effectSeed{path: "a.go", content: "worker\n", before: "before\n", branch: "job-1", session: "child", job: "job-1"})
		h.remove("a.go", "job-1")
		h.writeFile("a.go", "before\n")
		trunk := deletedDiff(t, h, "a.go")
		if trunk.State != "" || trunk.Diff != "" || trunk.VersionID == "" || trunk.Note == "" {
			t.Fatalf("trunk diff = %+v, want the live head untouched", trunk)
		}
		worker := h.tctx
		worker.Identity.SessionID, worker.Identity.WorkerJobID = "child", "job-1"
		worker.Source.SourceWorkspaceKind = api.SourceWorkspaceKindWorker
		out, err := h.tool.Run(t.Context(), map[string]any{"mode": "diff", "path": "a.go"}, worker)
		testutil.FailErr(t, "worker diff", err)
		var onBranch diffResult
		testutil.FailErr(t, "decode worker diff", json.Unmarshal([]byte(out), &onBranch))
		if onBranch.State != "absent" || !onBranch.Recorded {
			t.Fatalf("worker diff = %+v, want the deletion on its branch", onBranch)
		}
	})
	t.Run("never tracked keeps the no-record note", func(t *testing.T) {
		h := newHistoryHarness(t)
		h.beginSession(historySession)
		result := deletedDiff(t, h, "never.go")
		if result.Recorded || result.Note == "" || result.State != "" {
			t.Fatalf("untracked diff = %+v, want recorded=false with a note", result)
		}
	})
}

func TestSourceHistoryDiffUntrackedPathStatesNoRecord(t *testing.T) {
	h := newHistoryHarness(t)
	h.beginSession(historySession)
	result := h.sessionDiff("never.go")
	if result.Recorded || result.Note == "" {
		t.Fatalf("untracked diff = %+v, want recorded=false with a note", result)
	}
}

func TestSourceHistoryDiffExplicitPathRejections(t *testing.T) {
	h := newHistoryHarness(t)
	h.record(effectSeed{path: "a.go", content: "a\n", op: api.SourceChangeOpCreate})
	version := h.head("a.go").VersionID
	h.record(effectSeed{path: "other.go", content: "b\n", op: api.SourceChangeOpCreate})
	other := h.head("other.go").VersionID
	h.writeFile("a.go", "a\n")
	h.writeFile("untracked.go", "x\n")

	_, err := h.diff(map[string]any{"path": "a.go", "version_id": "missing"})
	expectReject(t, err, "SOURCE_VERSION_NOT_FOUND")
	_, err = h.diff(map[string]any{"path": "a.go", "version_id": other})
	expectReject(t, err, "SOURCE_VERSION_PATH_MISMATCH")
	_, err = h.diff(map[string]any{"path": "untracked.go", "version_id": version, "base_version_id": "head"})
	expectReject(t, err, "SOURCE_VERSION_NOT_FOUND")
}

func TestSourceHistoryDiffReportsLedgerFailures(t *testing.T) {
	failure := errors.New("ledger down")
	h := newHistoryHarness(t)
	h.record(effectSeed{path: "a.go", content: "a\n", op: api.SourceChangeOpCreate})
	h.writeFile("a.go", "a\n")
	real := h.store.History
	withFiles := func(files tools.SourceFileHistory, comparison tools.SourceComparison) {
		h.tctx.Source.History.Files, h.tctx.Source.History.Comparison = files, comparison
	}
	head := h.head("a.go")

	withFiles(failingDiffFiles{SourceFileHistory: real, headErr: failure}, h.store.Comparisons)
	if _, err := h.diff(map[string]any{"path": "a.go"}); !errors.Is(err, failure) {
		t.Fatalf("head failure = %v", err)
	}
	withFiles(failingDiffFiles{SourceFileHistory: real, headErr: sourceledger.ErrHistoryNotFound, deletedErr: failure}, h.store.Comparisons)
	if _, err := h.diff(map[string]any{"path": "a.go"}); !errors.Is(err, failure) {
		t.Fatalf("deleted lookup failure = %v", err)
	}
	withFiles(failingDiffFiles{SourceFileHistory: real, head: head, floorErr: failure}, h.store.Comparisons)
	if _, err := h.diff(map[string]any{"path": "a.go"}); !errors.Is(err, failure) {
		t.Fatalf("floor failure = %v", err)
	}
	withFiles(failingDiffFiles{SourceFileHistory: real, head: head, oldestErr: failure}, h.store.Comparisons)
	if _, err := h.diff(map[string]any{"path": "a.go"}); !errors.Is(err, failure) {
		t.Fatalf("baseline failure = %v", err)
	}
	withFiles(failingDiffFiles{SourceFileHistory: real, head: head, found: true, pathErr: failure}, h.store.Comparisons)
	if _, err := h.diff(map[string]any{"path": "a.go"}); !errors.Is(err, failure) {
		t.Fatalf("path baseline failure = %v", err)
	}
	withFiles(failingDiffFiles{SourceFileHistory: real, head: head, found: true}, fixedComparison{err: failure})
	if _, err := h.diff(map[string]any{"path": "a.go"}); !errors.Is(err, failure) {
		t.Fatalf("comparison failure = %v", err)
	}
	withFiles(failingDiffFiles{SourceFileHistory: real, head: head, found: true}, fixedComparison{err: sourceledger.ErrHistoryNotFound})
	_, err := h.diff(map[string]any{"path": "a.go"})
	expectReject(t, err, "SOURCE_VERSION_NOT_FOUND")

	h.tctx.Source.History = tools.SourceHistory{}
	_, err = h.diff(map[string]any{"path": "a.go"})
	expectReject(t, err, "SOURCE_HISTORY_UNAVAILABLE")
	_, err = h.diff(map[string]any{"path": "a.go", "version_id": "v"})
	expectReject(t, err, "SOURCE_HISTORY_UNAVAILABLE")
	_, err = h.diff(map[string]any{})
	expectReject(t, err, "TOOL_ARGS_INVALID")
}

type failingDiffFiles struct {
	tools.SourceFileHistory
	head                                     sourceledger.BranchHead
	found                                    bool
	headErr, deletedErr, floorErr, oldestErr error
	pathErr                                  error
}

func (f failingDiffFiles) ResolveHead(context.Context, string, sourcebranch.ID, string, string) (sourceledger.BranchHead, error) {
	return f.head, f.headErr
}

func (f failingDiffFiles) ResolveDeletedPath(context.Context, string, sourcebranch.ID, string, string) (sourceledger.DeletedPath, error) {
	return sourceledger.DeletedPath{}, f.deletedErr
}

func (f failingDiffFiles) SessionActivityFloor(context.Context, string, string) (int64, bool, error) {
	return 1, true, f.floorErr
}

func (f failingDiffFiles) OldestPathEffectAfterOnBranch(context.Context, string, sourcebranch.ID, string, string, int64) (sourceledger.FileEffectBounds, bool, error) {
	return sourceledger.FileEffectBounds{}, false, f.pathErr
}

func (f failingDiffFiles) OldestFileEffectAfterOnBranch(context.Context, string, sourcebranch.ID, string, int64) (sourceledger.FileEffectBounds, bool, error) {
	return sourceledger.FileEffectBounds{}, f.found, f.oldestErr
}
