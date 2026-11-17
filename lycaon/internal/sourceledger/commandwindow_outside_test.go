package sourceledger

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// faultySnapshots stands in for the outside snapshot port: it counts what it
// captures, can replace the scope, and can fail a capture of one folder.
type faultySnapshots struct {
	outsideSnapshotPort
	scope func(root string, omit []string) *sourcescope.Scope
	// fail answers the nth capture of root (1 is the baseline) when ok.
	fail func(n int, root string) (sourcesnapshot.Snapshot, error, bool)

	mu    sync.Mutex
	calls map[string]int
}

func (f *faultySnapshots) Ensure(ctx context.Context, req sourcesnapshot.Request) (sourcesnapshot.Snapshot, error) {
	root := req.Roots[0].Path
	f.mu.Lock()
	if f.calls == nil {
		f.calls = make(map[string]int)
	}
	f.calls[root]++
	n := f.calls[root]
	f.mu.Unlock()
	if f.fail != nil {
		if snapshot, err, ok := f.fail(n, root); ok {
			return snapshot, err
		}
	}
	return f.outsideSnapshotPort.Ensure(ctx, req)
}

func (f *faultySnapshots) OutsideScope(root string, omit []string) *sourcescope.Scope {
	if f.scope != nil {
		return f.scope(root, omit)
	}
	return f.outsideSnapshotPort.OutsideScope(root, omit)
}

func (f *faultySnapshots) captures() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, n := range f.calls {
		total += n
	}
	return total
}

func installSnapshots(store *Store, fault *faultySnapshots) *faultySnapshots {
	fault.outsideSnapshotPort = store.Commands.outsideSnapshots
	store.Commands.outsideSnapshots = fault
	return fault
}

// outsideDir is a folder beyond the project, spelled as the tools resolve it.
func outsideDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve outside folder", err)
	return dir
}

func outsideRel(t *testing.T, abs string) string {
	t.Helper()
	rel, ok := projectroot.OutsideRel(abs)
	if !ok {
		t.Fatalf("%s has no outside-namespace path", abs)
	}
	return rel
}

func openOutsideWindow(t *testing.T, store *Store, ctx context.Context, root string, outside ...string) *OpenCommandWindow {
	t.Helper()
	specs := make([]OutsideRootSpec, 0, len(outside))
	for _, path := range outside {
		specs = append(specs, OutsideRootSpec{Path: path})
	}
	window, err := store.Commands.OpenCommandWindow(ctx, CommandWindowInput{
		ProjectID: "p1", Roots: onDiskRoots(root), Outside: specs, SessionID: "s1", Turn: 3,
		ToolCallID: "call-1", ToolName: "command", CommandLine: "make install",
	})
	testutil.FailErr(t, "open command window", err)
	return window
}

// storedCoverage reads a window's coverage rows, keyed by folder path.
func storedCoverage(t *testing.T, store *Store, ctx context.Context, windowID string) map[string]FolderCoverage {
	t.Helper()
	raw, err := json.Marshal([]string{windowID})
	testutil.FailErr(t, "encode window id", err)
	byWindow, err := store.Commands.windowCoverage(ctx, string(raw))
	testutil.FailErr(t, "read window coverage", err)
	out := make(map[string]FolderCoverage)
	for _, folder := range byWindow[windowID] {
		out[folder.RootID+":"+folder.Path] = folder
	}
	return out
}

func outsideKey(outside, rel string) string { return outside + ":" + rel }

func sessionWalk(t *testing.T, store *Store, ctx context.Context) WalkResult {
	t.Helper()
	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{Kind: BaselineSession, SessionID: "s1"}, 100, 0, CommitLens{})
	testutil.FailErr(t, "walk the chat", err)
	return walk
}

func TestCommandWindowRecordsOutsideWriteRootChangesACFL16(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	insertChat(t, store, ctx, "s1", "")
	outside := outsideRoot(t, store, ctx)
	dir := outsideDir(t)
	writeRootFile(t, dir, "edit.conf", "a = 1\n")
	writeRootFile(t, dir, "gone.conf", "old\n")

	window := openOutsideWindow(t, store, ctx, root, dir)
	writeRootFile(t, dir, "edit.conf", "a = 2\n")
	writeRootFile(t, dir, "sub/new.conf", "fresh\n")
	testutil.FailErr(t, "remove gone.conf", os.Remove(filepath.Join(dir, "gone.conf")))
	closeAndSettle(t, ctx, window)

	effects := effectsByPath(sessionWalk(t, store, ctx))
	want := map[string]api.SourceChangeOp{
		outsideRel(t, filepath.Join(dir, "edit.conf")):    api.SourceChangeOpWrite,
		outsideRel(t, filepath.Join(dir, "sub/new.conf")): api.SourceChangeOpCreate,
		outsideRel(t, filepath.Join(dir, "gone.conf")):    api.SourceChangeOpDelete,
	}
	for path, op := range want {
		effect, ok := effects[path]
		if !ok {
			t.Fatalf("chat review lacks %s: %+v", path, effects)
		}
		if effect.RootID != outside || effect.Op != op || effect.CommandWindowID != window.ID || effect.Cause != CauseCommandWindow ||
			effect.CaptureQuality != CaptureReconciled || effect.SessionID != "s1" || effect.Turn != 3 || effect.ToolCallID != "call-1" {
			t.Fatalf("%s effect = %+v, want an observed %s linked to the window", path, effect, op)
		}
	}
	refs, err := store.queries.ListSourceChatFiles(ctx, "s1")
	testutil.FailErr(t, "list chat references", err)
	if len(refs) != len(want) {
		t.Fatalf("chat references = %+v, want one per changed outside file", refs)
	}
	coverage := storedCoverage(t, store, ctx, window.ID)
	if got := coverage[outsideKey(outside, outsideRel(t, dir))]; got.State != api.SourceWindowCoverageComplete {
		t.Fatalf("outside coverage = %+v, want complete", coverage)
	}
	if got := coverage["r1:"]; got.State != api.SourceWindowCoverageComplete {
		t.Fatalf("attached coverage = %+v, want complete", coverage)
	}
}

// A file a tool already recorded is not recorded again by the window.
func TestCommandWindowLeavesRecordedOutsideWritesAloneACFL16(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	insertChat(t, store, ctx, "s1", "")
	outside := outsideRoot(t, store, ctx)
	dir := outsideDir(t)
	window := openOutsideWindow(t, store, ctx, root, dir)
	writeRootFile(t, dir, "tool.txt", "by the tool\n")
	recordOutside(t, store, ctx, outside, "s1", outsideRel(t, filepath.Join(dir, "tool.txt")), "by the tool\n")
	closeAndSettle(t, ctx, window)
	var operations int
	testutil.FailErr(t, "count window operations", store.sqlDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM source_operations WHERE command_window_id = ?`, window.ID).Scan(&operations))
	if operations != 0 {
		t.Fatalf("window recorded %d operations for a write the tool recorded", operations)
	}
}

func TestOneFailingOutsideRootKeepsTheOthersChangesACFL17(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	insertChat(t, store, ctx, "s1", "")
	outside := outsideRoot(t, store, ctx)
	good, bad := outsideDir(t), outsideDir(t)
	installSnapshots(store, &faultySnapshots{fail: func(n int, folder string) (sourcesnapshot.Snapshot, error, bool) {
		return sourcesnapshot.Snapshot{}, errors.New("permission denied"), n == 2 && folder == bad
	}})
	window := openOutsideWindow(t, store, ctx, root, good, bad)
	writeRootFile(t, good, "kept.txt", "kept\n")
	writeRootFile(t, bad, "unknown.txt", "unknown\n")
	closeAndSettle(t, ctx, window)

	effects := effectsByPath(sessionWalk(t, store, ctx))
	if _, ok := effects[outsideRel(t, filepath.Join(good, "kept.txt"))]; !ok {
		t.Fatalf("the healthy root's change was lost: %+v", effects)
	}
	if _, ok := effects[outsideRel(t, filepath.Join(bad, "unknown.txt"))]; ok {
		t.Fatal("the failed root reported a change it could not observe")
	}
	coverage := storedCoverage(t, store, ctx, window.ID)
	if got := coverage[outsideKey(outside, outsideRel(t, good))]; got.State != api.SourceWindowCoverageComplete {
		t.Fatalf("healthy root coverage = %+v", got)
	}
	if got := coverage[outsideKey(outside, outsideRel(t, bad))]; got.State != api.SourceWindowCoverageNotObserved || got.Reason != api.SourceWindowGapIoError {
		t.Fatalf("failed root coverage = %+v, want not observed for an I/O error", got)
	}
}

func TestEachNotObservedReasonIsStoredDistinctlyACFL17(t *testing.T) {
	missing := sourcesnapshot.Snapshot{ID: "snapshot-the-store-never-held"}
	cases := []struct {
		reason api.SourceWindowGapReason
		fail   func(n int) (sourcesnapshot.Snapshot, error, bool)
		// unconfined marks the window's command unconfined.
		unconfined bool
	}{
		{reason: api.SourceWindowGapUnconfined, unconfined: true},
		{reason: api.SourceWindowGapBaselineIncomplete, fail: func(n int) (sourcesnapshot.Snapshot, error, bool) {
			return sourcesnapshot.Snapshot{}, context.DeadlineExceeded, n == 1
		}},
		{reason: api.SourceWindowGapCanceled, fail: func(n int) (sourcesnapshot.Snapshot, error, bool) {
			return sourcesnapshot.Snapshot{}, context.Canceled, n == 2
		}},
		{reason: api.SourceWindowGapIoError, fail: func(n int) (sourcesnapshot.Snapshot, error, bool) {
			return sourcesnapshot.Snapshot{}, os.ErrPermission, n == 2
		}},
		{reason: api.SourceWindowGapSettlementFailed, fail: func(n int) (sourcesnapshot.Snapshot, error, bool) {
			return missing, nil, n == 2
		}},
		{reason: api.SourceWindowGapTimeBudget, fail: func(n int) (sourcesnapshot.Snapshot, error, bool) {
			return sourcesnapshot.Snapshot{}, context.DeadlineExceeded, n == 2
		}},
	}
	for _, tc := range cases {
		t.Run(string(tc.reason), func(t *testing.T) {
			store, ctx, root := openLedgerOnDisk(t)
			insertChat(t, store, ctx, "s1", "")
			outside := outsideRoot(t, store, ctx)
			dir := outsideDir(t)
			if tc.fail != nil {
				installSnapshots(store, &faultySnapshots{fail: func(n int, _ string) (sourcesnapshot.Snapshot, error, bool) { return tc.fail(n) }})
			}
			window := openOutsideWindow(t, store, ctx, root, dir)
			if tc.unconfined {
				window.MarkUnconfined()
			}
			writeRootFile(t, dir, "changed.txt", "changed\n")
			closeAndSettle(t, ctx, window)

			key := outsideKey(outside, outsideRel(t, dir))
			if tc.unconfined {
				key = outsideKey(outside, "")
			}
			coverage := storedCoverage(t, store, ctx, window.ID)
			if got := coverage[key]; got.State != api.SourceWindowCoverageNotObserved || got.Reason != tc.reason || len(got.Boundaries) != 0 {
				t.Fatalf("coverage = %+v, want not observed for %s", coverage, tc.reason)
			}
			walk := sessionWalk(t, store, ctx)
			if len(walk.Files) != 0 {
				t.Fatalf("an unobserved folder produced file rows: %+v", walk.Files)
			}
			// The window recorded no change, and still says what it could not see.
			if len(walk.Commands) != 1 || walk.Commands[0].ID != window.ID || len(walk.Commands[0].Coverage) == 0 {
				t.Fatalf("walk commands = %+v, want the window with its coverage", walk.Commands)
			}
		})
	}
}

func boundedScope(budgets sandbox.SurveyBudgets, floor ...string) func(string, []string) *sourcescope.Scope {
	return func(root string, _ []string) *sourcescope.Scope {
		return sourcescope.New(root, sourcescope.Options{Plane: sourcescope.Plane{Budgets: budgets}, Floor: floor})
	}
}

func writeEntries(t *testing.T, dir, sub string, count int) {
	t.Helper()
	for i := range count {
		writeRootFile(t, dir, filepath.ToSlash(filepath.Join(sub, "f"+strconv.Itoa(i)+".txt")), "entry "+strconv.Itoa(i)+"\n")
	}
}

// settledOutsideCoverage runs one window over dir with the given scope and
// returns the folder's coverage and the files its last capture held.
func settledOutsideCoverage(t *testing.T, dir string, scope func(string, []string) *sourcescope.Scope) (FolderCoverage, int) {
	t.Helper()
	store, ctx, root := openLedgerOnDisk(t)
	outside := outsideRoot(t, store, ctx)
	var captured int
	fault := installSnapshots(store, &faultySnapshots{scope: scope})
	fault.fail = func(n int, folder string) (sourcesnapshot.Snapshot, error, bool) {
		if n != 2 {
			return sourcesnapshot.Snapshot{}, nil, false
		}
		snapshot, err := fault.outsideSnapshotPort.Ensure(ctx, sourcesnapshot.Request{Roots: []sourcesnapshot.Root{{Path: folder}}, Scope: scope(folder, nil)})
		captured = snapshot.FileCount
		return snapshot, err, true
	}
	window := openOutsideWindow(t, store, ctx, root, dir)
	closeAndSettle(t, ctx, window)
	return storedCoverage(t, store, ctx, window.ID)[outsideKey(outside, outsideRel(t, dir))], captured
}

func TestEntryBudgetsBoundOutsideObservationACFL19(t *testing.T) {
	const limit = 6
	cases := []struct {
		name     string
		budgets  sandbox.SurveyBudgets
		sub      string
		boundary api.SourceWindowBoundaryReason
		path     string
	}{
		{name: "walk_entries", budgets: sandbox.SurveyBudgets{WalkEntries: limit}, boundary: api.SourceWindowBoundaryWalkBudget, path: "."},
		{name: "directory_entries", budgets: sandbox.SurveyBudgets{DirectoryEntries: limit}, sub: "d", boundary: api.SourceWindowBoundaryDirectoryCap, path: "d"},
		{name: "subtree_entries", budgets: sandbox.SurveyBudgets{SubtreeEntries: limit, WalkEntries: 100}, sub: "d", boundary: api.SourceWindowBoundarySubtreeCap, path: "d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			atLimit := outsideDir(t)
			// Every entry counts against the budget; this folder holds exactly the limit.
			writeEntries(t, atLimit, tc.sub, limit)
			coverage, _ := settledOutsideCoverage(t, atLimit, boundedScope(tc.budgets))
			if coverage.State != api.SourceWindowCoverageComplete {
				t.Fatalf("at the limit coverage = %+v, want complete", coverage)
			}
			over := outsideDir(t)
			writeEntries(t, over, tc.sub, limit+1)
			coverage, captured := settledOutsideCoverage(t, over, boundedScope(tc.budgets))
			if coverage.State != api.SourceWindowCoveragePartial || len(coverage.Boundaries) != 1 ||
				coverage.Boundaries[0].Reason != tc.boundary || coverage.Boundaries[0].Path != tc.path {
				t.Fatalf("over the limit coverage = %+v, want partial at %s for %s", coverage, tc.path, tc.boundary)
			}
			if captured > limit {
				t.Fatalf("capture held %d files past the %d-entry budget", captured, limit)
			}
		})
	}
}

func TestPartialBoundaryReasonsAreStoredWithTheirPathsACFL17(t *testing.T) {
	t.Run("scope", func(t *testing.T) {
		dir := outsideDir(t)
		writeRootFile(t, dir, "node_modules/pkg/index.js", "x\n")
		writeRootFile(t, dir, "kept.txt", "kept\n")
		coverage, _ := settledOutsideCoverage(t, dir, boundedScope(sandbox.SurveyBudgets{WalkEntries: 100}, "node_modules"))
		if coverage.State != api.SourceWindowCoveragePartial || len(coverage.Boundaries) != 1 ||
			coverage.Boundaries[0] != (CoverageBoundary{Path: "node_modules", Reason: api.SourceWindowBoundaryScope}) {
			t.Fatalf("coverage = %+v, want partial at node_modules for scope", coverage)
		}
	})
	t.Run("unreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads every folder")
		}
		dir := outsideDir(t)
		writeRootFile(t, dir, "locked/secret.txt", "x\n")
		testutil.FailErr(t, "lock folder", os.Chmod(filepath.Join(dir, "locked"), 0))
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "locked"), 0o750) })
		coverage, _ := settledOutsideCoverage(t, dir, boundedScope(sandbox.SurveyBudgets{WalkEntries: 100}))
		if coverage.State != api.SourceWindowCoveragePartial || len(coverage.Boundaries) != 1 ||
			coverage.Boundaries[0] != (CoverageBoundary{Path: "locked", Reason: api.SourceWindowBoundaryUnreadable}) {
			t.Fatalf("coverage = %+v, want partial at locked for unreadable", coverage)
		}
	})
}

// A window over a folder that holds an attached root never walks that root.
func TestOutsideRootSkipsTheFoldersItOmitsACFL19(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	insertChat(t, store, ctx, "s1", "")
	outside := outsideRoot(t, store, ctx)
	dir := outsideDir(t)
	window, err := store.Commands.OpenCommandWindow(ctx, CommandWindowInput{
		ProjectID: "p1", Roots: onDiskRoots(root), SessionID: "s1", Turn: 1, ToolName: "command", CommandLine: "make",
		Outside: []OutsideRootSpec{{Path: dir, Omit: []string{filepath.Join(dir, "elsewhere")}}},
	})
	testutil.FailErr(t, "open command window", err)
	writeRootFile(t, dir, "elsewhere/skipped.txt", "skipped\n")
	writeRootFile(t, dir, "seen.txt", "seen\n")
	closeAndSettle(t, ctx, window)
	effects := effectsByPath(sessionWalk(t, store, ctx))
	if _, ok := effects[outsideRel(t, filepath.Join(dir, "elsewhere/skipped.txt"))]; ok {
		t.Fatal("an omitted folder was observed")
	}
	if _, ok := effects[outsideRel(t, filepath.Join(dir, "seen.txt"))]; !ok {
		t.Fatalf("the folder's own change is missing: %+v", effects)
	}
	if got := storedCoverage(t, store, ctx, window.ID)[outsideKey(outside, outsideRel(t, dir))]; got.State != api.SourceWindowCoverageComplete {
		t.Fatalf("coverage = %+v, want complete: omitted folders are not gaps", got)
	}
}

func TestWindowWithoutOutsideRootsWalksNothingBeyondTheProjectACFL19(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	fault := installSnapshots(store, &faultySnapshots{})
	window := openOutsideWindow(t, store, ctx, root)
	closeAndSettle(t, ctx, window)
	if n := fault.captures(); n != 0 {
		t.Fatalf("a window with no outside write root captured %d outside folders", n)
	}
	for key := range storedCoverage(t, store, ctx, window.ID) {
		if key != "r1:" {
			t.Fatalf("coverage names %s beyond the attached root", key)
		}
	}
}

func TestChangesPastTheBeforeImageLimitsKeepTheirRecordACFL18(t *testing.T) {
	for _, limit := range []string{"files", "bytes"} {
		t.Run(limit, func(t *testing.T) {
			store, ctx, root := openLedgerOnDisk(t)
			insertChat(t, store, ctx, "s1", "")
			dir := outsideDir(t)
			const files = 4
			for i := range files {
				writeRootFile(t, dir, "f"+strconv.Itoa(i)+".txt", "before "+strconv.Itoa(i)+"\n")
			}
			if limit == "files" {
				store.Commands.retention.files = 1
			} else {
				store.Commands.retention.bytes = int64(len("before 0\n"))
			}
			window := openOutsideWindow(t, store, ctx, root, dir)
			for i := range files {
				writeRootFile(t, dir, "f"+strconv.Itoa(i)+".txt", "after "+strconv.Itoa(i)+"\n")
			}
			closeAndSettle(t, ctx, window)

			rows, err := store.sqlDB.QueryContext(ctx, `SELECT v.capture_state, v.capture_reason FROM source_effects e
				JOIN source_operations o ON o.id = e.operation_id
				JOIN source_versions v ON v.id = e.before_version_id
				WHERE o.command_window_id = ?`, window.ID)
			testutil.FailErr(t, "read before versions", err)
			defer func() { _ = rows.Close() }()
			stored, metadataOnly := 0, 0
			for rows.Next() {
				var state, reason string
				testutil.FailErr(t, "scan before version", rows.Scan(&state, &reason))
				switch {
				case state == "stored":
					stored++
				case state == "metadata_only" && reason == "content_not_captured":
					metadataOnly++
				default:
					t.Fatalf("before version %s/%s", state, reason)
				}
			}
			testutil.FailErr(t, "read before versions", rows.Err())
			if stored != 1 || metadataOnly != files-1 {
				t.Fatalf("before versions: %d stored, %d metadata only; want 1 and %d", stored, metadataOnly, files-1)
			}
		})
	}
}
