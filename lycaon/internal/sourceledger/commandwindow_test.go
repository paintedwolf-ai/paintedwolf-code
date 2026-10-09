package sourceledger

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type diskLedger struct {
	store      *Store
	sqlDB      db.Handle
	contentDir string
	root       string
}

// openLedgerOnDisk binds root r1 to a real folder so inventory passes can
// observe it.
func openLedgerOnDisk(t *testing.T) (*Store, context.Context, string) {
	t.Helper()
	ledger := openDiskLedger(t)
	return ledger.store, context.Background(), ledger.root
}

// openDiskLedger also keeps the content dir so a second store can share it.
func openDiskLedger(t *testing.T) diskLedger {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, sqlDB, "p1")
	root := t.TempDir()
	_, err := sqlDB.ExecContext(t.Context(), `
		INSERT INTO project_roots (id, project_id, path, label, is_primary, added_at, kind)
		VALUES ('r1', 'p1', ?, 'root', 1, ?, 'attached')
	`, root, db.FormatTime(time.Now().UTC()))
	testutil.FailErr(t, "insert root", err)
	contentDir := t.TempDir()
	store := New(sqlDB, contentDir)
	// These tests deliver the command's writes themselves, so a closed window
	// that still owes a pass runs it at once instead of waiting out the settle period.
	store.Commands.windowSettle = 0
	return diskLedger{store: store, sqlDB: sqlDB, contentDir: contentDir, root: root}
}

func writeRootFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	testutil.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(abs), 0o750))
	testutil.FailErr(t, "write "+rel, os.WriteFile(abs, []byte(content), 0o640))
}

func onDiskRoots(root string) []RootSpec {
	return []RootSpec{{ID: "r1", Path: root}}
}

func openTestWindow(t *testing.T, store *Store, ctx context.Context, root, commandLine string) *OpenCommandWindow {
	t.Helper()
	window, err := store.Commands.OpenCommandWindow(ctx, CommandWindowInput{
		ProjectID: "p1", Roots: onDiskRoots(root), SessionID: "s1", Turn: 3,
		ToolCallID: "call-1", ToolName: "command", CommandLine: commandLine,
	})
	testutil.FailErr(t, "open command window", err)
	return window
}

// deliverWrites hands changed paths to the ledger the way the watcher does
// once its events arrive, so a pass does not depend on delivery timing.
func deliverWrites(ctx context.Context, root string, rels ...string) {
	repochange.Notify(ctx, repochange.Event{
		ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: rels, Source: repochange.SourceWatcher,
	})
}

// closeAndSettle ends a window and waits for its row to be final.
func closeAndSettle(t *testing.T, ctx context.Context, window *OpenCommandWindow) {
	t.Helper()
	testutil.FailErr(t, "close command window", window.Close(ctx))
	select {
	case <-window.Settled():
	case <-time.After(10 * time.Second):
		t.Fatal("command window did not settle")
	}
}

func effectsByPath(walk WalkResult) map[string]Effect {
	out := make(map[string]Effect)
	for _, file := range walk.Files {
		for _, effect := range file.Effects {
			out[effect.Path] = effect
		}
	}
	return out
}

func TestCommandWindowAdmitsWhatTheCommandChanged(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	writeRootFile(t, root, "src/lib.rs", "pub fn a() {}\n")
	writeRootFile(t, root, "old.txt", "stale\n")
	writeRootFile(t, root, "untouched.txt", "same\n")

	window := openTestWindow(t, store, ctx, root, "cargo build")

	// The command regenerates a lockfile, rewrites an untracked source file,
	// and removes another; one file stays as it was.
	writeRootFile(t, root, "Cargo.lock", "[[package]]\nname = \"a\"\n")
	writeRootFile(t, root, "src/lib.rs", "pub fn a() {}\npub fn b() {}\n")
	testutil.FailErr(t, "remove old.txt", os.Remove(filepath.Join(root, "old.txt")))
	deliverWrites(ctx, root, "Cargo.lock", "src/lib.rs", "old.txt")
	closeAndSettle(t, ctx, window)

	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{Kind: BaselineTurn, SessionID: "s1", Turn: 3}, 50, 0, CommitLens{})
	testutil.FailErr(t, "query turn walk", err)
	got := effectsByPath(walk)
	if len(got) != 3 {
		t.Fatalf("turn effects = %+v, want Cargo.lock, src/lib.rs, old.txt", got)
	}
	for path, op := range map[string]api.SourceChangeOp{
		"Cargo.lock": api.SourceChangeOpCreate,
		"src/lib.rs": api.SourceChangeOpWrite,
		"old.txt":    api.SourceChangeOpDelete,
	} {
		effect, ok := got[path]
		if !ok {
			t.Fatalf("missing effect for %s in %+v", path, got)
		}
		if effect.Op != op || effect.Origin != api.SourceChangeOriginExternal ||
			effect.Cause != CauseCommandWindow || effect.CaptureQuality != CaptureReconciled ||
			effect.CommandWindowID != window.ID || effect.SessionID != "s1" || effect.Turn != 3 ||
			effect.ToolCallID != "call-1" || effect.ToolName != "command" {
			t.Fatalf("%s effect = %+v", path, effect)
		}
	}
	if len(walk.Commands) != 1 || walk.Commands[0].ID != window.ID ||
		walk.Commands[0].CommandLine != "cargo build" || walk.Commands[0].State != api.SourceCommandWindowEnded ||
		walk.Commands[0].EndedTS.IsZero() || walk.Commands[0].AdmissionMode == "" {
		t.Fatalf("walk commands = %+v", walk.Commands)
	}

	// The rewritten file's comparison spans the start bytes to the end bytes.
	comparison, err := store.Comparisons.CompareEffect(ctx, "p1", got["src/lib.rs"].ID)
	testutil.FailErr(t, "compare rewritten file", err)
	if comparison.Before.Content != "pub fn a() {}\n" || comparison.After.Content != "pub fn a() {}\npub fn b() {}\n" {
		t.Fatalf("rewrite comparison = %+v", comparison)
	}
	deletion, err := store.Comparisons.CompareEffect(ctx, "p1", got["old.txt"].ID)
	testutil.FailErr(t, "compare deletion", err)
	if deletion.Before.Content != "stale\n" || deletion.After.State != "absent" {
		t.Fatalf("deletion comparison = %+v", deletion)
	}

	// The window rides the file's version listing and the session's authorship.
	versions, err := store.History.QueryFileVersions(ctx, "p1", got["Cargo.lock"].FileID, 10, 0)
	testutil.FailErr(t, "query versions", err)
	if len(versions.Versions) == 0 || versions.Versions[0].CommandWindowID != window.ID {
		t.Fatalf("versions = %+v", versions.Versions)
	}
	if _, ok := versions.CommandWindows[window.ID]; !ok {
		t.Fatalf("version windows = %+v", versions.CommandWindows)
	}
	authored, err := store.Walk.SessionAuthoredPaths(ctx, "p1", "s1", "r1")
	testutil.FailErr(t, "session authored paths", err)
	if len(authored) != 3 {
		t.Fatalf("session authored paths = %v", authored)
	}
}

func TestCommandWindowThatObservedNothingLeavesNoRow(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	writeRootFile(t, root, "main.go", "package main\n")
	window := openTestWindow(t, store, ctx, root, "go test ./...")
	closeAndSettle(t, ctx, window)

	if _, err := store.queries.GetSourceCommandWindow(ctx, window.ID); err == nil {
		t.Fatal("window with no effects kept its row")
	}
	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{Kind: BaselineSession, SessionID: "s1"}, 50, 0, CommitLens{})
	testutil.FailErr(t, "query session walk", err)
	if len(walk.Files) != 0 || len(walk.Commands) != 0 {
		t.Fatalf("walk after a quiet command = %+v", walk)
	}
}

func TestDriftIsAttributedToTheOpenWindowAndOutsideAppOtherwise(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	writeRootFile(t, root, "config.toml", "a = 1\n")
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "config.toml",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent,
		SessionID: "s1", Turn: 1, After: []byte("a = 1\n"),
	})

	// The first pass only seeds tracking; drift with no window open is
	// outside the app.
	req, err := store.Commands.windowInventoryRequest(ctx, "p1", onDiskRoots(root))
	testutil.FailErr(t, "inventory request", err)
	req.Force, req.Wait = true, true
	testutil.FailErr(t, "seed tracking", store.Inventory.EnsureInventory(ctx, req))
	writeRootFile(t, root, "config.toml", "a = 2\n")
	deliverWrites(ctx, root, "config.toml")
	testutil.FailErr(t, "reconcile outside a window", store.Inventory.EnsureInventory(ctx, req))

	window := openTestWindow(t, store, ctx, root, "sed -i s/2/3/ config.toml")
	writeRootFile(t, root, "config.toml", "a = 3\n")
	deliverWrites(ctx, root, "config.toml")
	closeAndSettle(t, ctx, window)

	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{}, 50, 0, CommitLens{})
	testutil.FailErr(t, "query walk", err)
	if len(walk.Files) != 1 || len(walk.Files[0].Effects) != 3 {
		t.Fatalf("walk = %+v", walk.Files)
	}
	effects := walk.Files[0].Effects
	inWindow, outside := effects[0], effects[1]
	if inWindow.Cause != CauseCommandWindow || inWindow.CommandWindowID != window.ID ||
		inWindow.SessionID != "s1" || inWindow.Turn != 3 || inWindow.ActorLabel != "" {
		t.Fatalf("in-window drift = %+v", inWindow)
	}
	if outside.Cause != CauseFilesystemReconcile || outside.CommandWindowID != "" ||
		outside.SessionID != "" || outside.ActorLabel != "Outside app" {
		t.Fatalf("outside drift = %+v", outside)
	}
}

func TestWindowStillAttributesUntilItSettles(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	writeRootFile(t, root, "main.go", "package main\n")
	window := openTestWindow(t, store, ctx, root, "go generate ./...")
	writeRootFile(t, root, "gen/types.go", "package gen\n")
	deliverWrites(ctx, root, "gen/types.go")
	// The window keeps settling until the watcher's pass lands.
	store.Commands.windowSettle = time.Hour
	testutil.FailErr(t, "close command window", window.Close(ctx))

	// The watcher's pass for the command's last writes lands after the
	// process exited; the window still names it.
	req, err := store.Commands.windowInventoryRequest(ctx, "p1", onDiskRoots(root))
	testutil.FailErr(t, "inventory request", err)
	req.Force, req.Wait = true, true
	testutil.FailErr(t, "late watcher pass", store.Inventory.EnsureInventory(ctx, req))
	select {
	case <-window.Settled():
	case <-time.After(10 * time.Second):
		t.Fatal("command window did not settle")
	}
	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{Kind: BaselineTurn, SessionID: "s1", Turn: 3}, 50, 0, CommitLens{})
	testutil.FailErr(t, "query turn walk", err)
	got := effectsByPath(walk)
	if effect, ok := got["gen/types.go"]; !ok || effect.CommandWindowID != window.ID {
		t.Fatalf("late pass effects = %+v", got)
	}
}

func TestRunningWindowOutranksASettlingOne(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	writeRootFile(t, root, "main.go", "package main\n")
	first := openTestWindow(t, store, ctx, root, "make first")
	// The first window is still settling when the second one's pass lands.
	store.Commands.windowSettle = time.Hour
	testutil.FailErr(t, "close first", first.Close(ctx))
	second := openTestWindow(t, store, ctx, root, "make second")
	writeRootFile(t, root, "second.out", "2\n")
	deliverWrites(ctx, root, "second.out")
	req, err := store.Commands.windowInventoryRequest(ctx, "p1", onDiskRoots(root))
	testutil.FailErr(t, "inventory request", err)
	req.Force, req.Wait = true, true
	testutil.FailErr(t, "pass while the first window settles", store.Inventory.EnsureInventory(ctx, req))
	select {
	case <-first.Settled():
	case <-time.After(10 * time.Second):
		t.Fatal("first window did not settle")
	}
	store.Commands.windowSettle = 0
	closeAndSettle(t, ctx, second)
	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{}, 50, 0, CommitLens{})
	testutil.FailErr(t, "query walk", err)
	got := effectsByPath(walk)
	if effect, ok := got["second.out"]; !ok || effect.CommandWindowID != second.ID {
		t.Fatalf("second command's file = %+v, want window %s", got, second.ID)
	}
	if _, err := store.queries.GetSourceCommandWindow(ctx, first.ID); err == nil {
		t.Fatal("quiet first window kept a row")
	}
}

func TestUntrackedFilesStayOutsideHistoryWithoutAWindow(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	writeRootFile(t, root, "seed.txt", "seed\n")
	req, err := store.Commands.windowInventoryRequest(ctx, "p1", onDiskRoots(root))
	testutil.FailErr(t, "inventory request", err)
	req.Force, req.Wait = true, true
	testutil.FailErr(t, "seed tracking", store.Inventory.EnsureInventory(ctx, req))

	writeRootFile(t, root, "generated.txt", "outside\n")
	deliverWrites(ctx, root, "generated.txt")
	testutil.FailErr(t, "reconcile outside a window", store.Inventory.EnsureInventory(ctx, req))

	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{}, 50, 0, CommitLens{})
	testutil.FailErr(t, "query walk", err)
	if len(walk.Files) != 0 {
		t.Fatalf("untracked file entered history without a window: %+v", walk.Files)
	}
}

func TestMidWindowPassAttributesAndCloseDoesNotDuplicate(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	writeRootFile(t, root, "main.go", "package main\n")
	window := openTestWindow(t, store, ctx, root, "make generate")

	writeRootFile(t, root, "gen/out.go", "package gen\n")
	deliverWrites(ctx, root, "gen/out.go")
	req, err := store.Commands.windowInventoryRequest(ctx, "p1", onDiskRoots(root))
	testutil.FailErr(t, "inventory request", err)
	req.Force, req.Wait = true, true
	testutil.FailErr(t, "watcher pass during the command", store.Inventory.EnsureInventory(ctx, req))
	closeAndSettle(t, ctx, window)

	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{Kind: BaselineTurn, SessionID: "s1", Turn: 3}, 50, 0, CommitLens{})
	testutil.FailErr(t, "query turn walk", err)
	if len(walk.Files) != 1 || len(walk.Files[0].Effects) != 1 ||
		walk.Files[0].Effects[0].CommandWindowID != window.ID {
		t.Fatalf("walk = %+v", walk.Files)
	}
}

func TestInterruptedWindowsSettleWhenAFreshProcessOpensOne(t *testing.T) {
	ledger := openDiskLedger(t)
	store, ctx, root := ledger.store, context.Background(), ledger.root
	writeRootFile(t, root, "main.go", "package main\n")
	orphan := openTestWindow(t, store, ctx, root, "npm run dev")
	writeRootFile(t, root, "public/bundle.js", "//\n")
	deliverWrites(ctx, root, "public/bundle.js")
	req, err := store.Commands.windowInventoryRequest(ctx, "p1", onDiskRoots(root))
	testutil.FailErr(t, "inventory request", err)
	req.Force, req.Wait = true, true
	testutil.FailErr(t, "pass during the orphan", store.Inventory.EnsureInventory(ctx, req))
	quiet := openTestWindow(t, store, ctx, root, "true")

	// A new process holds no memory of either window.
	fresh := New(ledger.sqlDB, ledger.contentDir)
	fresh.Commands.windowSettle = 0
	next, err := fresh.Commands.OpenCommandWindow(ctx, CommandWindowInput{
		ProjectID: "p1", Roots: onDiskRoots(root), SessionID: "s2", Turn: 1,
		ToolName: "command", CommandLine: "cargo build",
	})
	testutil.FailErr(t, "open window in fresh process", err)
	closeAndSettle(t, ctx, next)

	row, err := fresh.queries.GetSourceCommandWindow(ctx, orphan.ID)
	testutil.FailErr(t, "read orphan window", err)
	if row.State != string(api.SourceCommandWindowInterrupted) || row.EndedTs == "" {
		t.Fatalf("orphan window = %+v, want interrupted", row)
	}
	if _, err := fresh.queries.GetSourceCommandWindow(ctx, quiet.ID); err == nil {
		t.Fatal("unreferenced orphan window kept its row")
	}
}

func TestWindowRowRecordsItsOwnClockPosition(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	window := openTestWindow(t, store, ctx, root, "touch a")
	writeRootFile(t, root, "a", "1\n")
	deliverWrites(ctx, root, "a")
	closeAndSettle(t, ctx, window)
	row, err := store.queries.GetSourceCommandWindow(ctx, window.ID)
	testutil.FailErr(t, "read window", err)
	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{}, 50, 0, CommitLens{})
	testutil.FailErr(t, "query walk", err)
	if len(walk.Files) != 1 || walk.Files[0].Effects[0].Ordinal <= row.Ordinal {
		t.Fatalf("window ordinal %d does not precede its effect: %+v", row.Ordinal, walk.Files)
	}
	var operations int64
	testutil.FailErr(t, "count operations", store.sqlDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM source_operations WHERE command_window_id = ?`, window.ID).Scan(&operations))
	if operations != 1 {
		t.Fatalf("operations naming the window = %d, want 1", operations)
	}
}

// A window retains the before-images git and history cannot answer for
// only until it settles; what it admitted lives on as versions.
func TestCommandWindowReleasesItsStartRetentionWhenSettled(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	writeRootFile(t, root, "gen.txt", "before\n")
	writeRootFile(t, root, "same.txt", "same\n")

	window := openTestWindow(t, store, ctx, root, "make gen")
	pins := func() int {
		var count int
		testutil.FailErr(t, "count window objects", store.sqlDB.QueryRowContext(ctx,
			`SELECT count(*) FROM source_command_window_objects WHERE window_id = ?`, window.ID).Scan(&count))
		return count
	}
	if got := pins(); got != 2 {
		t.Fatalf("retained objects while open = %d, want 2", got)
	}
	writeRootFile(t, root, "gen.txt", "after\n")
	deliverWrites(ctx, root, "gen.txt")
	closeAndSettle(t, ctx, window)
	if got := pins(); got != 0 {
		t.Fatalf("retained objects after settle = %d, want 0", got)
	}
	testutil.FailErr(t, "maintain blobs", store.Retention.MaintainBlobs(ctx))

	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{Kind: BaselineTurn, SessionID: "s1", Turn: 3}, 50, 0, CommitLens{})
	testutil.FailErr(t, "query walk", err)
	effect, ok := effectsByPath(walk)["gen.txt"]
	if !ok {
		t.Fatalf("gen.txt not admitted: %+v", effectsByPath(walk))
	}
	comparison, err := store.Comparisons.CompareEffect(ctx, "p1", effect.ID)
	testutil.FailErr(t, "compare", err)
	if comparison.Before.Content != "before\n" || comparison.After.Content != "after\n" {
		t.Fatalf("comparison = %+v", comparison)
	}
}
