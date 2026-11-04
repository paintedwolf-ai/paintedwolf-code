package survey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	historyProject = "p1"
	historySession = "s1"
)

// historyHarness runs source_history over a real ledger with a second root
// available and the session's first turn boundary already recorded.
type historyHarness struct {
	t      *testing.T
	store  *sourceledger.Store
	dir    string
	tool   *SourceHistoryTool
	tctx   tools.ToolContext
	nextOp int
}

func newHistoryHarness(t *testing.T) *historyHarness {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, sqlDB, historyProject)
	dir := t.TempDir()
	second := t.TempDir()
	now := db.FormatTime(time.Now().UTC())
	_, err := sqlDB.ExecContext(t.Context(), `
		INSERT INTO project_roots (id, project_id, path, label, is_primary, added_at, kind)
		VALUES ('r1', 'p1', ?, 'root', 1, ?, 'attached'), ('r2', 'p1', ?, 'docs', 0, ?, 'attached')
	`, dir, now, second, now)
	testutil.FailErr(t, "insert roots", err)
	store := sourceledger.New(sqlDB, t.TempDir())
	tctx := nativefixture.Context(dir)
	tctx.Identity.ProjectID, tctx.Identity.SessionID = historyProject, historySession
	tctx.Source.Roots = append(tctx.Source.Roots, projectroot.RootRef{ID: "r2", Label: "docs", Path: second})
	tctx.Source.History = tools.SourceHistory{Files: store.History, Comparison: store.Comparisons, Git: store.Git, Authorship: store.Walk}
	return &historyHarness{t: t, store: store, dir: dir, tool: &SourceHistoryTool{Boundary: nativefixture.Boundary(t)}, tctx: tctx}
}

func (h *historyHarness) beginSession(sessionID string) {
	h.t.Helper()
	_, err := h.store.Checkpoints.CreateStructuralCheckpoint(h.t.Context(), sourceledger.StructuralCheckpointInput{
		ProjectID: historyProject, Kind: sourceledger.CheckpointTurn, Label: "Turn start", SessionID: sessionID, Turn: 1,
	})
	testutil.FailErr(h.t, "create turn checkpoint", err)
}

type effectSeed struct {
	root, path, fromPath string
	branch               sourcebranch.ID
	session, job         string
	origin               api.SourceChangeOrigin
	op                   api.SourceChangeOp
	content              string
}

func (h *historyHarness) record(seed effectSeed) {
	h.t.Helper()
	h.nextOp++
	if seed.root == "" {
		seed.root = "r1"
	}
	if seed.origin == "" {
		seed.origin = api.SourceChangeOriginAgent
		if seed.session == "" {
			seed.session = historySession
		}
	}
	if seed.op == "" {
		seed.op = api.SourceChangeOpWrite
	}
	input := sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: seed.root, Path: seed.path, FromPath: seed.fromPath},
		ProjectID:      historyProject, BranchID: seed.branch, Op: seed.op, Origin: seed.origin,
		SessionID: seed.session, JobID: seed.job, Turn: 1,
		OperationID: fmt.Sprintf("op-%d", h.nextOp),
	}
	if seed.content != "" {
		input.After = []byte(seed.content)
	}
	testutil.FailErr(h.t, "record effect", h.store.Record(h.t.Context(), input))
}

func (h *historyHarness) head(path string) sourceledger.BranchHead {
	h.t.Helper()
	head, err := h.store.History.ResolveHead(h.t.Context(), historyProject, sourcebranch.Trunk, "r1", path)
	testutil.FailErr(h.t, "resolve head", err)
	return head
}

func (h *historyHarness) writeFile(rel, content string) {
	h.t.Helper()
	testutil.FailErr(h.t, "write file", os.WriteFile(filepath.Join(h.dir, rel), []byte(content), 0o644))
}

type overviewResult struct {
	Mode     string  `json:"mode"`
	Recorded bool    `json:"recorded"`
	Note     string  `json:"note"`
	Count    int     `json:"count"`
	Branch   *string `json:"branch"`
	Since    *struct {
		Ordinal int64 `json:"ordinal"`
	} `json:"since"`
	Truncated bool `json:"truncated"`
	Files     []struct {
		Path      string `json:"path"`
		Op        string `json:"op"`
		Actor     string `json:"actor"`
		At        string `json:"at"`
		Effects   int    `json:"effects"`
		VersionID string `json:"version_id"`
		Branch    string `json:"branch"`
		WindowID  string `json:"window_id"`
		Settled   *bool  `json:"settled"`
		During    bool   `json:"during_your_command"`
	} `json:"files"`
	OpenWindows []struct {
		ID      string `json:"id"`
		Running bool   `json:"running"`
	} `json:"open_windows"`
}

func runOverviewCall(t *testing.T, tool *SourceHistoryTool, tctx tools.ToolContext) overviewResult {
	t.Helper()
	out, err := tool.Run(t.Context(), map[string]any{}, tctx)
	testutil.FailErr(t, "source_history()", err)
	var result overviewResult
	testutil.FailErr(t, "decode overview", json.Unmarshal([]byte(out), &result))
	if result.Mode != "overview" {
		t.Fatalf("mode = %q, want overview", result.Mode)
	}
	return result
}

func TestSourceHistoryOverviewACOV01ListsEveryActorPerFile(t *testing.T) {
	h := newHistoryHarness(t)
	h.record(effectSeed{path: "before.go", content: "earlier session\n", session: "s0"})
	h.beginSession(historySession)
	floor, _, err := h.store.History.SessionActivityFloor(t.Context(), historyProject, historySession)
	testutil.FailErr(t, "resolve floor", err)
	h.record(effectSeed{path: "a.go", content: "one\n", op: api.SourceChangeOpCreate})
	h.record(effectSeed{path: "b.go", content: "one\n", op: api.SourceChangeOpCreate})
	h.record(effectSeed{path: "a.go", content: "two\n"})
	h.record(effectSeed{path: "user.go", content: "theirs\n", origin: api.SourceChangeOriginUser, op: api.SourceChangeOpCreate})

	result := runOverviewCall(t, h.tool, h.tctx)
	if !result.Recorded || result.Truncated || result.Since == nil || result.Since.Ordinal != floor || result.Count != 3 {
		t.Fatalf("overview = %+v, want recorded rows since %d", result, floor)
	}
	type row struct {
		path, actor string
		effects     int
	}
	want := []row{{"user.go", "user", 1}, {"a.go", "you", 2}, {"b.go", "you", 1}}
	if len(result.Files) != len(want) {
		t.Fatalf("files = %+v, want %+v", result.Files, want)
	}
	for i, file := range result.Files {
		if got := (row{file.Path, file.Actor, file.Effects}); got != want[i] {
			t.Fatalf("row %d = %+v, want %+v", i, got, want[i])
		}
		if file.At == "" || file.VersionID == "" {
			t.Fatalf("row %d has no time or version: %+v", i, file)
		}
	}
	if newest := h.head("a.go"); result.Files[1].VersionID != newest.VersionID || result.Files[1].Op != string(api.SourceChangeOpWrite) {
		t.Fatalf("a.go row = %+v, want its newest effect at %s", result.Files[1], newest.VersionID)
	}
}

func TestSourceHistoryOverviewACOV02UnknownWindowListsNothing(t *testing.T) {
	h := newHistoryHarness(t)
	h.record(effectSeed{path: "a.go", content: "one\n"})
	result := runOverviewCall(t, h.tool, h.tctx)
	if result.Recorded || result.Note == "" || len(result.Files) != 0 || result.Since != nil {
		t.Fatalf("unknown window = %+v, want recorded=false with a note and no rows", result)
	}
}

func TestSourceHistoryOverviewACOV03OverflowIsAFloor(t *testing.T) {
	h := newHistoryHarness(t)
	h.beginSession(historySession)
	for i := range overviewEffectCap + 100 {
		h.record(effectSeed{path: fmt.Sprintf("f%d.go", i%450), content: fmt.Sprintf("v%d\n", i)})
	}
	result := runOverviewCall(t, h.tool, h.tctx)
	if !result.Truncated || result.Note == "" {
		t.Fatalf("overflow truncated=%v note=%q, want a floor note", result.Truncated, result.Note)
	}
	effects := 0
	for _, file := range result.Files {
		effects += file.Effects
	}
	if effects != overviewEffectCap || result.Count != len(result.Files) {
		t.Fatalf("rows hold %d effects across %d files (count %d), want the cap", effects, len(result.Files), result.Count)
	}
}

func TestSourceHistoryOverviewACOV04WorkerSeesItsBranchOnly(t *testing.T) {
	h := newHistoryHarness(t)
	h.beginSession(historySession)
	h.beginSession("child")
	h.record(effectSeed{path: "trunk.go", content: "trunk\n"})
	h.record(effectSeed{path: "mine.go", content: "worker\n", branch: "job-1", session: "child", job: "job-1"})
	h.record(effectSeed{path: "other.go", content: "peer\n", branch: "job-2", session: "peer", job: "job-2"})

	worker := h.tctx
	worker.Identity.SessionID, worker.Identity.WorkerJobID = "child", "job-1"
	worker.Source.SourceWorkspaceKind = api.SourceWorkspaceKindWorker
	result := runOverviewCall(t, h.tool, worker)
	if len(result.Files) != 1 || result.Files[0].Path != "mine.go" || result.Files[0].Actor != "you" {
		t.Fatalf("worker rows = %+v, want its own branch only", result.Files)
	}
	if result.Branch == nil || *result.Branch != "job-1" {
		t.Fatalf("worker branch = %v, want job-1", result.Branch)
	}

	coordinator := runOverviewCall(t, h.tool, h.tctx)
	if len(coordinator.Files) != 1 || coordinator.Files[0].Path != "trunk.go" {
		t.Fatalf("trunk rows = %+v, want trunk only", coordinator.Files)
	}
}

func TestSourceHistoryOverviewACOV05SecondaryRootIsQualified(t *testing.T) {
	h := newHistoryHarness(t)
	h.beginSession(historySession)
	h.record(effectSeed{root: "r2", path: "guide.md", content: "doc\n"})
	h.record(effectSeed{path: "a.go", content: "code\n"})
	result := runOverviewCall(t, h.tool, h.tctx)
	if len(result.Files) != 2 || result.Files[0].Path != "a.go" || result.Files[1].Path != "@docs/guide.md" {
		t.Fatalf("rows = %+v, want a.go and @docs/guide.md", result.Files)
	}
}

// A worktree binding can place one root on its own branch while another stays on
// trunk; each root must answer for its own branch only.
func TestSourceHistoryOverviewReadsEachRootOnItsOwnBranch(t *testing.T) {
	h := newHistoryHarness(t)
	h.beginSession(historySession)
	worktree := sourcebranch.ForWorktree("w1")
	h.record(effectSeed{path: "on-worktree.go", content: "in\n", branch: worktree})
	h.record(effectSeed{path: "on-trunk-r1.go", content: "out\n"})
	h.record(effectSeed{root: "r2", path: "on-trunk-r2.md", content: "in\n"})
	h.record(effectSeed{root: "r2", path: "on-worktree-r2.md", content: "out\n", branch: worktree})

	tctx := h.tctx
	tctx.Source.ProjectRootBranches = map[string]sourcebranch.ID{"r1": worktree, "r2": sourcebranch.Trunk}
	result := runOverviewCall(t, h.tool, tctx)
	if result.Branch != nil {
		t.Fatalf("branch = %q, want none when the roots differ", *result.Branch)
	}
	if len(result.Files) != 2 ||
		result.Files[0].Path != "@docs/on-trunk-r2.md" || result.Files[0].Branch != "" ||
		result.Files[1].Path != "on-worktree.go" || result.Files[1].Branch != worktree.String() {
		t.Fatalf("rows = %+v, want each root's own branch", result.Files)
	}
}

func TestSourceHistoryOverviewACOV06EffectsModeStillNeedsAPath(t *testing.T) {
	h := newHistoryHarness(t)
	h.beginSession(historySession)
	_, err := h.tool.Run(t.Context(), map[string]any{"mode": "effects"}, h.tctx)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "TOOL_ARGS_INVALID" || reject.Data["field"] != "path" {
		t.Fatalf("effects without path = %v, want the missing path rejection", err)
	}
}

func TestSourceHistoryOverviewACOV07AndACOV10MineStaysAuthoredPaths(t *testing.T) {
	h := newHistoryHarness(t)
	h.beginSession(historySession)
	h.record(effectSeed{path: "mine.go", content: "mine\n"})
	h.record(effectSeed{path: "user.go", content: "theirs\n", origin: api.SourceChangeOriginUser})

	out, err := h.tool.Run(t.Context(), map[string]any{"mode": "mine"}, h.tctx)
	testutil.FailErr(t, "source_history mine", err)
	var mine map[string]json.RawMessage
	testutil.FailErr(t, "decode mine", json.Unmarshal([]byte(out), &mine))
	var paths []string
	testutil.FailErr(t, "decode mine paths", json.Unmarshal(mine["paths"], &paths))
	if len(paths) != 1 || paths[0] != "mine.go" {
		t.Fatalf("mine paths = %v, want the authored path only", paths)
	}
	if _, has := mine["files"]; has {
		t.Fatalf("mine carries files: %s", out)
	}

	overview, err := h.tool.Run(t.Context(), map[string]any{}, h.tctx)
	testutil.FailErr(t, "source_history()", err)
	var raw map[string]json.RawMessage
	testutil.FailErr(t, "decode overview", json.Unmarshal([]byte(overview), &raw))
	if _, has := raw["files"]; !has {
		t.Fatalf("overview has no files: %s", overview)
	}
	if _, has := raw["paths"]; has {
		t.Fatalf("overview carries paths: %s", overview)
	}
}

func TestSourceHistoryOverviewACOV13ExplicitModeMatchesTheDefault(t *testing.T) {
	h := newHistoryHarness(t)
	h.beginSession(historySession)
	h.record(effectSeed{path: "a.go", content: "one\n"})
	want := runOverviewCall(t, h.tool, h.tctx)
	if len(want.Files) != 1 {
		t.Fatalf("default overview = %+v", want)
	}
	for name, args := range map[string]map[string]any{
		"mode alone":      {"mode": "overview"},
		"path is ignored": {"mode": "overview", "path": "../outside/never.go"},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := h.tool.Run(t.Context(), args, h.tctx)
			testutil.FailErr(t, "source_history overview", err)
			var got overviewResult
			testutil.FailErr(t, "decode overview", json.Unmarshal([]byte(out), &got))
			if got.Mode != "overview" || !got.Recorded || len(got.Files) != 1 || got.Files[0].Path != "a.go" {
				t.Fatalf("overview = %+v, want the same rows as the default", got)
			}
		})
	}
}

func TestSourceHistoryOverviewACOV09WorkerFloodNeitherFillsNorTruncates(t *testing.T) {
	h := newHistoryHarness(t)
	h.beginSession(historySession)
	for i := range 3 {
		h.record(effectSeed{path: fmt.Sprintf("trunk%d.go", i), content: "trunk\n"})
	}
	for i := range 500 {
		job := fmt.Sprintf("job-%d", i%4)
		h.record(effectSeed{path: "shared.go", content: fmt.Sprintf("v%d\n", i), branch: sourcebranch.ID(job), session: "peer", job: job})
	}
	result := runOverviewCall(t, h.tool, h.tctx)
	if len(result.Files) != 3 || result.Truncated || result.Note != "" {
		t.Fatalf("overview = %+v, want three trunk rows and no floor", result)
	}
}

// windowLedger answers the overview from fixed effects so window facts can be
// driven directly; SessionWindow channels stand in for settlement.
type windowLedger struct {
	tools.SourceFileHistory
	effects []sourceledger.Effect
	onRead  func()
}

func (l *windowLedger) SessionActivityFloor(context.Context, string, string) (int64, bool, error) {
	return 1, true, nil
}

func (l *windowLedger) EffectsBetweenOnBranch(context.Context, string, map[string]sourcebranch.ID, int64, int64, int) ([]sourceledger.Effect, error) {
	if l.onRead != nil {
		l.onRead()
	}
	return l.effects, nil
}

// windowListing returns successive listings, repeating the last.
type windowListing struct {
	listings [][]sourceledger.SessionWindow
}

func (w *windowListing) OpenSessionWindows(string, string) []sourceledger.SessionWindow {
	current := w.listings[0]
	if len(w.listings) > 1 {
		w.listings = w.listings[1:]
	}
	return current
}

func windowContext(t *testing.T, files *windowLedger, windows tools.SourceWindowReader) tools.ToolContext {
	t.Helper()
	tctx := nativefixture.Context(t.TempDir())
	tctx.Identity.ProjectID, tctx.Identity.SessionID = historyProject, historySession
	store := newHistoryHarness(t).store
	tctx.Source.History = tools.SourceHistory{Files: files, Comparison: store.Comparisons, Git: store.Git, Authorship: store.Walk, Windows: windows}
	return tctx
}

func windowEffect(path, windowID string) sourceledger.Effect {
	return sourceledger.Effect{FileID: path, RootID: "r1", Path: path, Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginExternal, CommandWindowID: windowID, AfterVersionID: "v-" + path}
}

func TestSourceHistoryOverviewACOV08RowsStateTheirWindow(t *testing.T) {
	settledRow := windowEffect("settled.go", "w-settled")
	openRow := windowEffect("open.go", "w-open")
	lateRow := windowEffect("late.go", "w-late")
	plainRow := windowEffect("plain.go", "")
	files := &windowLedger{effects: []sourceledger.Effect{settledRow, openRow, lateRow, plainRow}}
	never := make(chan struct{})
	listing := &windowListing{listings: [][]sourceledger.SessionWindow{
		{{ID: "w-open", Settled: never}},
		{{ID: "w-open", Settled: never}},
		// Opened after the wait; its row is still not settled.
		{{ID: "w-open", Settled: never}, {ID: "w-late", Settled: never}},
	}}
	tool := &SourceHistoryTool{}
	result := runOverviewCall(t, tool, windowContext(t, files, listing))
	want := map[string]struct {
		window  string
		settled *bool
	}{
		"settled.go": {"w-settled", new(true)},
		"open.go":    {"w-open", new(false)},
		"late.go":    {"w-late", new(false)},
		"plain.go":   {"", nil},
	}
	for _, file := range result.Files {
		expected := want[file.Path]
		if file.WindowID != expected.window || (file.Settled == nil) != (expected.settled == nil) ||
			(file.Settled != nil && *file.Settled != *expected.settled) {
			t.Fatalf("row %+v, want %+v", file, expected)
		}
	}
	if len(result.Files) != 4 || len(result.OpenWindows) != 2 || result.Note == "" {
		t.Fatalf("overview = %+v, want four rows, two open windows and a floor note", result)
	}

	unread := runOverviewCall(t, tool, windowContext(t, files, nil))
	if len(unread.OpenWindows) != 0 || unread.Note != "" {
		t.Fatalf("nil reader overview = %+v, want no window facts", unread)
	}
	for _, file := range unread.Files {
		if file.WindowID != "" || file.Settled != nil {
			t.Fatalf("nil reader row = %+v, want no window facts", file)
		}
	}
}

// The fact names the viewer's own window; it does not change the actor class.
func TestSourceHistoryOverviewMarksEffectsDuringYourCommand(t *testing.T) {
	own := windowEffect("own.go", "w-own")
	own.SessionID = historySession
	foreign := windowEffect("foreign.go", "w-peer")
	foreign.SessionID = "peer"
	drift := windowEffect("drift.go", "")
	drift.SessionID = historySession
	result := runOverviewCall(t, &SourceHistoryTool{}, windowContext(t, &windowLedger{effects: []sourceledger.Effect{own, foreign, drift}}, nil))
	want := map[string]bool{"own.go": true, "foreign.go": false, "drift.go": false}
	if len(result.Files) != len(want) {
		t.Fatalf("files = %+v", result.Files)
	}
	for _, file := range result.Files {
		if file.During != want[file.Path] || file.Actor != "external" {
			t.Fatalf("row %+v, want during_your_command %v and actor external", file, want[file.Path])
		}
	}
}

func TestSourceHistoryOverviewACOV11WaitsForAClosingWindow(t *testing.T) {
	settled := make(chan struct{})
	readSawSettled := false
	files := &windowLedger{
		effects: []sourceledger.Effect{windowEffect("gen.txt", "w1")},
		onRead: func() {
			select {
			case <-settled:
				readSawSettled = true
			default:
			}
		},
	}
	listing := &windowListing{listings: [][]sourceledger.SessionWindow{
		{{ID: "w1", Closing: true, Settled: settled}},
		nil,
	}}
	waited := false
	tool := &SourceHistoryTool{settleExpiry: func() <-chan time.Time {
		waited = true
		close(settled)
		return nil
	}}
	result := runOverviewCall(t, tool, windowContext(t, files, listing))
	if !waited || !readSawSettled {
		t.Fatalf("waited=%v readSawSettled=%v, want effects read after settlement", waited, readSawSettled)
	}
	if len(result.OpenWindows) != 0 || result.Note != "" {
		t.Fatalf("settled overview = %+v, want no open window and no floor note", result)
	}
	if len(result.Files) != 1 || result.Files[0].WindowID != "w1" || result.Files[0].Settled == nil || !*result.Files[0].Settled {
		t.Fatalf("rows = %+v, want the program's row settled", result.Files)
	}
}

func TestSourceHistoryOverviewACOV12DoesNotWaitOnWhatCannotSettle(t *testing.T) {
	never := make(chan struct{})
	t.Run("running job", func(t *testing.T) {
		listing := &windowListing{listings: [][]sourceledger.SessionWindow{{{ID: "job", Settled: never}}}}
		tool := &SourceHistoryTool{settleExpiry: func() <-chan time.Time {
			t.Error("started the settle budget for a running window")
			return nil
		}}
		result := runOverviewCall(t, tool, windowContext(t, &windowLedger{}, listing))
		if len(result.OpenWindows) != 1 || result.OpenWindows[0].ID != "job" || !result.OpenWindows[0].Running || result.Note == "" {
			t.Fatalf("overview = %+v, want the running window listed with a floor note", result)
		}
	})
	t.Run("closing past the budget", func(t *testing.T) {
		listing := &windowListing{listings: [][]sourceledger.SessionWindow{{{ID: "slow", Closing: true, Settled: never}}}}
		expired := make(chan time.Time)
		close(expired)
		tool := &SourceHistoryTool{settleExpiry: func() <-chan time.Time { return expired }}
		result := runOverviewCall(t, tool, windowContext(t, &windowLedger{}, listing))
		if len(result.OpenWindows) != 1 || result.OpenWindows[0].ID != "slow" || result.OpenWindows[0].Running || result.Note == "" {
			t.Fatalf("overview = %+v, want the closing window listed with a floor note", result)
		}
	})
	t.Run("canceled call", func(t *testing.T) {
		tool := &SourceHistoryTool{settleExpiry: func() <-chan time.Time { return nil }}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		tool.awaitClosingWindows(ctx, []sourceledger.SessionWindow{{ID: "slow", Closing: true, Settled: never}})
	})
}

func TestSourceHistoryOverviewReportsLedgerFailures(t *testing.T) {
	failure := errors.New("ledger down")
	tctx := nativefixture.Context(t.TempDir())
	tctx.Identity.ProjectID, tctx.Identity.SessionID = historyProject, historySession
	store := newHistoryHarness(t).store
	history := tools.SourceHistory{Comparison: store.Comparisons, Git: store.Git, Authorship: store.Walk}

	t.Run("no ledger", func(t *testing.T) {
		_, err := (&SourceHistoryTool{}).Run(t.Context(), nil, tctx)
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "SOURCE_HISTORY_UNAVAILABLE" {
			t.Fatalf("no ledger = %v", err)
		}
	})
	t.Run("floor", func(t *testing.T) {
		tctx := tctx
		tctx.Source.History = history
		tctx.Source.History.Files = failingFloor{err: failure}
		if _, err := (&SourceHistoryTool{}).Run(t.Context(), nil, tctx); !errors.Is(err, failure) {
			t.Fatalf("floor failure = %v", err)
		}
	})
	t.Run("effects", func(t *testing.T) {
		tctx := tctx
		tctx.Source.History = history
		tctx.Source.History.Files = failingEffects{err: failure}
		if _, err := (&SourceHistoryTool{}).Run(t.Context(), nil, tctx); !errors.Is(err, failure) {
			t.Fatalf("effects failure = %v", err)
		}
	})
	t.Run("branch", func(t *testing.T) {
		tctx := tctx
		tctx.Source.History = history
		tctx.Source.History.Files = failingEffects{}
		tctx.Source.SourceWorkspaceKind = api.SourceWorkspaceKindWorker
		if _, err := (&SourceHistoryTool{}).Run(t.Context(), nil, tctx); err == nil {
			t.Fatal("a worker without a job resolved a branch")
		}
	})
}

type failingFloor struct {
	tools.SourceFileHistory
	err error
}

func (f failingFloor) SessionActivityFloor(context.Context, string, string) (int64, bool, error) {
	return 0, false, f.err
}

type failingEffects struct {
	tools.SourceFileHistory
	err error
}

func (f failingEffects) SessionActivityFloor(context.Context, string, string) (int64, bool, error) {
	return 1, true, nil
}

func (f failingEffects) EffectsBetweenOnBranch(context.Context, string, map[string]sourcebranch.ID, int64, int64, int) ([]sourceledger.Effect, error) {
	return nil, f.err
}
