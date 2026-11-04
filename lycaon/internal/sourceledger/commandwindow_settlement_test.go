package sourceledger

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// holdSettlePeriods keeps every settle period running until the returned func ends them.
func holdSettlePeriods(store *Store) func() {
	gate := make(chan time.Time)
	store.Commands.SetWindowSettleGateForTest(gate)
	return sync.OnceFunc(func() { close(gate) })
}

// beforePassCompletes runs before inside a pass, after it has captured the tree.
type beforePassCompletes struct {
	inventoryCommandsPort
	before func()
}

func (p beforePassCompletes) notePassCompleted(projectID string, roots []RootSpec) {
	p.before()
	p.inventoryCommandsPort.notePassCompleted(projectID, roots)
}

// A pass that captured before the last write says nothing about it, so the close
// still reconciles what landed after that capture.
func TestPassInFlightAtCloseDoesNotLoseTrailingOutput(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	repochange.MarkCoverageCompleteForTest(root)
	t.Cleanup(func() { repochange.CloseRoot(root) })
	writeRootFile(t, root, "seed.txt", "seed\n")
	window := openTestWindow(t, store, ctx, root, "./gen.sh")
	endSettle := holdSettlePeriods(store)
	store.Inventory.commands = beforePassCompletes{store.Inventory.commands, sync.OnceFunc(func() {
		writeRootFile(t, root, "gen.txt", "generated\n")
		deliverWrites(ctx, root, "gen.txt")
		testutil.FailErr(t, "close during the pass", window.Close(ctx))
	})}
	forceTestPass(t, store, ctx, root)
	if window.IsSettled() {
		t.Fatal("a pass that predates the output ended the wait")
	}
	endSettle()
	testutil.AwaitClosed(t, "the window settles", window.Settled())
	if !window.IsSettled() {
		t.Fatal("window did not settle")
	}
	if effect := newestEffects(t, store, ctx)["gen.txt"]; effect.CommandWindowID != window.ID {
		t.Fatalf("trailing output = %+v, want the window", effect)
	}
}

// A pass on another branch observed another tree.
func TestPassOnAnotherBranchDoesNotEndTheWindowsWait(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	repochange.MarkCoverageCompleteForTest(root)
	t.Cleanup(func() { repochange.CloseRoot(root) })
	writeRootFile(t, root, "seed.txt", "seed\n")
	window := openTestWindow(t, store, ctx, root, "./gen.sh")
	endSettle := holdSettlePeriods(store)

	writeRootFile(t, root, "gen.txt", "generated\n")
	deliverWrites(ctx, root, "gen.txt")
	testutil.FailErr(t, "close window", window.Close(ctx))
	other := t.TempDir()
	testutil.FailErr(t, "write other tree", os.WriteFile(filepath.Join(other, "other.txt"), []byte("other\n"), 0o600))
	testutil.FailErr(t, "pass on another branch", store.Inventory.EnsureInventory(ctx, InventoryRequest{
		ProjectID: "p1", Roots: []RootSpec{{ID: "r1", BranchID: sourcebranch.ID("worktree:other"), Path: other}}, Force: true, Wait: true,
	}))
	if window.IsSettled() {
		t.Fatal("a pass on another branch ended the wait")
	}
	endSettle()
	testutil.AwaitClosed(t, "the window settles", window.Settled())
	if effect := newestEffects(t, store, ctx)["gen.txt"]; effect.CommandWindowID != window.ID {
		t.Fatalf("output = %+v, want the window", effect)
	}
}

func sessionAuthored(t *testing.T, store *Store, ctx context.Context, session string) []string {
	t.Helper()
	authored, err := store.Walk.SessionAuthoredPaths(ctx, "p1", session, "r1")
	testutil.FailErr(t, "session authored paths", err)
	return authored
}

// A window's claim on a file lasts until the file's next change by anyone else;
// what the agent itself wrote stays the session's.
func TestSomeoneElsesEditEndsAWindowsClaimOnTheFile(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	writeRootFile(t, root, "seed.txt", "seed\n")
	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "written.txt"}, ProjectID: "p1",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, SessionID: "s1", Turn: 1, After: []byte("agent\n"),
	})
	writeRootFile(t, root, "written.txt", "agent\n")
	idled := idleObserver(store)
	window, release := programWindow(t, store, ctx, root)
	writeRootFile(t, root, "gen.txt", "generated\n")
	writeRootFile(t, root, "kept.txt", "generated\n")
	deliverWrites(ctx, root, "gen.txt", "kept.txt")
	release()
	testutil.Receive(t, "the window goes idle", idled)
	if authored := sessionAuthored(t, store, ctx, "s1"); !slices.Contains(authored, "gen.txt") || !slices.Contains(authored, "kept.txt") {
		t.Fatalf("authored before the edit = %v", authored)
	}

	writeRootFile(t, root, "gen.txt", "a person's edit\n")
	writeRootFile(t, root, "written.txt", "a person's edit\n")
	deliverWrites(ctx, root, "gen.txt", "written.txt")
	forceTestPass(t, store, ctx, root)
	effects := newestEffects(t, store, ctx)
	wantOutsideEffect(t, effects, "gen.txt")
	wantOutsideEffect(t, effects, "written.txt")
	authored := sessionAuthored(t, store, ctx, "s1")
	slices.Sort(authored)
	if !slices.Equal(authored, []string{"kept.txt", "written.txt"}) {
		t.Fatalf("authored after the edit = %v, want the window's other file and the agent's write", authored)
	}
	closeAndSettle(t, ctx, window)
}

// A launch after a gap pays its own anchor even where a slow pass just finished,
// because that pass predates the gap.
func TestReanchorDoesNotReuseASlowPassOverTheGap(t *testing.T) {
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	store, ctx, root := openLedgerOnDisk(t)
	clock := manualLedgerClock(store)
	writeRootFile(t, root, "seed.txt", "seed\n")
	idled := idleObserver(store)
	window, first := programWindow(t, store, ctx, root)

	real := store.Commands.inventory
	released := make(chan struct{})
	close(released)
	store.Commands.inventory = &gatedInventory{commandsInventoryPort: real, release: released, during: func() {
		clock.advance(store.Commands.observationBudget + time.Second)
	}}
	first()
	testutil.Receive(t, "the slow grace pass ends", idled)
	store.Commands.inventory = real

	writeRootFile(t, root, "notes.md", "a person's save\n")
	second, err := window.Hold(ctx)
	testutil.FailErr(t, "hold after the gap", err)
	writeRootFile(t, root, "second.txt", "second\n")
	second()
	testutil.Receive(t, "the window goes idle again", idled)
	effects := newestEffects(t, store, ctx)
	wantOutsideEffect(t, effects, "notes.md")
	if effects["second.txt"].CommandWindowID != window.ID {
		t.Fatalf("second process's output = %+v, want the window", effects["second.txt"])
	}
	closeAndSettle(t, ctx, window)
}

// An event still on its way when a window opens belongs to before the window.
func TestOpenWaitsForAWatcherEventStillInFlight(t *testing.T) {
	// With no real watcher only the event below moves the token, so without the
	// wait the open has no pass of its own to wait through.
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	store, ctx, root := openLedgerOnDisk(t)
	repochange.MarkCoverageCompleteForTest(root)
	t.Cleanup(func() { repochange.CloseRoot(root) })
	writeRootFile(t, root, "seed.txt", "seed\n")
	forceTestPass(t, store, ctx, root)
	// Recovery's first writes must not outlast the event's delivery.
	testutil.FailErr(t, "recover windows", store.Commands.RecoverInterruptedWindows(ctx))

	writeRootFile(t, root, "notes.md", "a person's save\n")
	repochange.NotifyWorktreeDebounced(ctx, root, []string{"notes.md"}, repochange.SourceWatcher)
	window := openTestWindow(t, store, ctx, root, "make")
	repochange.WaitQuiet(ctx, root, 150*time.Millisecond, 750*time.Millisecond)
	closeAndSettle(t, ctx, window)
	if authored := sessionAuthored(t, store, ctx, "s1"); len(authored) != 0 {
		t.Fatalf("authored = %v, want the save before the window left out", authored)
	}
}

// deadlineRecorder reports whether a settling pass runs under a deadline.
type deadlineRecorder struct {
	commandsInventoryPort
	bounded chan bool
}

func (d deadlineRecorder) EnsureInventory(ctx context.Context, req InventoryRequest) error {
	_, bounded := ctx.Deadline()
	d.bounded <- bounded
	return d.commandsInventoryPort.EnsureInventory(ctx, req)
}

// A pass that hangs must not leave a closed window claiming drift forever.
func TestTrustedCloseSettlesUnderADeadline(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	repochange.MarkCoverageCompleteForTest(root)
	t.Cleanup(func() { repochange.CloseRoot(root) })
	window := openTestWindow(t, store, ctx, root, "make")
	recorder := deadlineRecorder{store.Commands.inventory, make(chan bool, 1)}
	store.Commands.inventory = recorder
	closeAndSettle(t, ctx, window)
	if !testutil.Receive(t, "the settling pass", recorder.bounded) {
		t.Fatal("the settling pass has no deadline")
	}
}

// A failure after the row is written leaves no running row for a window that
// never opened.
func TestRetentionFailureEndsTheRow(t *testing.T) {
	ledger := openDiskLedger(t)
	store, ctx, root := ledger.store, context.Background(), ledger.root
	writeRootFile(t, root, "a.txt", "a\n")
	_, err := ledger.sqlDB.ExecContext(ctx, `CREATE TRIGGER refuse_pins BEFORE INSERT ON source_command_window_objects BEGIN SELECT RAISE(ABORT, 'pins refused'); END`)
	testutil.FailErr(t, "create trigger", err)
	window, err := store.Commands.OpenCommandWindow(ctx, CommandWindowInput{ProjectID: "p1", Roots: onDiskRoots(root), SessionID: "s1", ToolName: "command"})
	if err == nil {
		t.Fatalf("window %s opened with its retention refused", window.ID)
	}
	var running int
	testutil.FailErr(t, "count running rows", ledger.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM source_command_windows WHERE state = 'running'`).Scan(&running))
	if running != 0 {
		t.Fatalf("running rows after the failed open = %d", running)
	}
}
