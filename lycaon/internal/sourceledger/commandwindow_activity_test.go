package sourceledger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func forceTestPass(t *testing.T, store *Store, ctx context.Context, root string) {
	t.Helper()
	req, err := store.Commands.windowInventoryRequest(ctx, "p1", onDiskRoots(root))
	testutil.FailErr(t, "inventory request", err)
	req.Force, req.Wait = true, true
	testutil.FailErr(t, "force inventory pass", store.Inventory.EnsureInventory(ctx, req))
}

func newestEffects(t *testing.T, store *Store, ctx context.Context) map[string]Effect {
	t.Helper()
	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{}, 200, 0, CommitLens{})
	testutil.FailErr(t, "query walk", err)
	out := make(map[string]Effect)
	for _, file := range walk.Files {
		for _, effect := range file.Effects {
			if current, seen := out[effect.Path]; !seen || effect.Ordinal > current.Ordinal {
				out[effect.Path] = effect
			}
		}
	}
	return out
}

func wantOutsideEffect(t *testing.T, effects map[string]Effect, path string) {
	t.Helper()
	effect, ok := effects[path]
	if !ok {
		t.Fatalf("%s did not reach history: %+v", path, effects)
	}
	if effect.CommandWindowID != "" || effect.SessionID != "" || effect.Origin != api.SourceChangeOriginExternal ||
		effect.Cause != CauseFilesystemReconcile || effect.ActorLabel != "Outside app" {
		t.Fatalf("%s effect = %+v, want a plain outside change", path, effect)
	}
}

func openSessionWindow(t *testing.T, store *Store, ctx context.Context, root, session string) *OpenCommandWindow {
	t.Helper()
	window, err := store.Commands.OpenCommandWindow(ctx, CommandWindowInput{
		ProjectID: "p1", Roots: onDiskRoots(root), SessionID: session, Turn: 1, ToolName: "command",
	})
	testutil.FailErr(t, "open window for "+session, err)
	return window
}

// programWindow opens a window the way a program does: its one launch holds it.
func programWindow(t *testing.T, store *Store, ctx context.Context, root string) (*OpenCommandWindow, func()) {
	t.Helper()
	window := openTestWindow(t, store, ctx, root, "./gen.sh; sleep 600")
	release, err := window.Hold(ctx)
	testutil.FailErr(t, "hold window", err)
	window.ReleaseOpener(ctx)
	return window, release
}

func idleObserver(store *Store) <-chan string {
	idled := make(chan string, 4)
	store.Commands.SetWindowIdledForTest(func(id string) { idled <- id })
	return idled
}

func TestWindowClaimsDriftOnlyUntilItsLastProcessSettles(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	writeRootFile(t, root, "seed.txt", "seed\n")
	idled := idleObserver(store)
	window, release := programWindow(t, store, ctx, root)

	writeRootFile(t, root, "gen.txt", "generated\n")
	deliverWrites(ctx, root, "gen.txt")
	release()
	if id := testutil.AwaitValue(t, "the window goes idle", idled); id != window.ID {
		t.Fatalf("idled window = %s, want %s", id, window.ID)
	}
	if effect := newestEffects(t, store, ctx)["gen.txt"]; effect.CommandWindowID != window.ID {
		t.Fatalf("the last process's output = %+v, want the window, landed by the grace's pass", effect)
	}

	// Both a file with a head and one without go to nobody during the gap.
	writeRootFile(t, root, "gen.txt", "a person's edit\n")
	writeRootFile(t, root, "notes.md", "a person's note\n")
	deliverWrites(ctx, root, "gen.txt", "notes.md")
	forceTestPass(t, store, ctx, root)
	effects := newestEffects(t, store, ctx)
	wantOutsideEffect(t, effects, "gen.txt")
	wantOutsideEffect(t, effects, "notes.md")
	listed := store.Commands.OpenSessionWindows("p1", "s1")
	if len(listed) != 1 || listed[0].Closing {
		t.Fatalf("an idle window must stay registered until its program settles: %+v", listed)
	}

	closeAndSettle(t, ctx, window)
	if !window.IsSettled() {
		t.Fatal("closing an idle window did not settle it")
	}
}

func TestHoldsCountConcurrentProcesses(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	idled := idleObserver(store)
	window, first := programWindow(t, store, ctx, root)
	second, err := window.Hold(ctx)
	testutil.FailErr(t, "second hold", err)
	first()
	first() // Releasing twice must not end the other process's hold.
	writeRootFile(t, root, "during.txt", "live\n")
	deliverWrites(ctx, root, "during.txt")
	forceTestPass(t, store, ctx, root)
	if effect := newestEffects(t, store, ctx)["during.txt"]; effect.CommandWindowID != window.ID {
		t.Fatalf("drift with one process still live = %+v, want the window", effect)
	}
	second()
	testutil.AwaitValue(t, "the window goes idle", idled)
	closeAndSettle(t, ctx, window)
	if _, err := window.Hold(ctx); !errors.Is(err, ErrWindowClosed) {
		t.Fatalf("hold after close = %v, want ErrWindowClosed", err)
	}
}

// A new process during the grace ends that grace, so the window stays active once
// the grace's settle period has passed, and only the new process's end idles it.
func TestHoldDuringTheGraceKeepsTheWindowActive(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	idled := idleObserver(store)
	endSettle := holdSettlePeriods(store)
	window, first := programWindow(t, store, ctx, root)
	first()
	second, err := window.Hold(ctx)
	testutil.FailErr(t, "hold during the grace", err)
	endSettle()
	for _, path := range []string{"during.txt", "still-during.txt"} {
		writeRootFile(t, root, path, "live\n")
		deliverWrites(ctx, root, path)
		forceTestPass(t, store, ctx, root)
		if effect := newestEffects(t, store, ctx)[path]; effect.CommandWindowID != window.ID {
			t.Fatalf("drift after a hold during the grace = %+v, want the window", effect)
		}
	}
	if len(idled) != 0 {
		t.Fatal("the ended grace idled a window with a live process")
	}
	second()
	testutil.Receive(t, "the window goes idle", idled)
	closeAndSettle(t, ctx, window)
}

// A pass nobody could claim moves every window's start, so a deletion recorded as
// an outside change is not claimed again by the session left active afterwards.
func TestUnclaimedPassMovesStartsPastADeletion(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	writeRootFile(t, root, "old.txt", "stale\n")
	first := openSessionWindow(t, store, ctx, root, "a")
	second := openSessionWindow(t, store, ctx, root, "b")
	testutil.FailErr(t, "remove old.txt", os.Remove(filepath.Join(root, "old.txt")))
	deliverWrites(ctx, root, "old.txt")
	forceTestPass(t, store, ctx, root)
	wantOutsideEffect(t, newestEffects(t, store, ctx), "old.txt")

	closeAndSettle(t, ctx, second)
	writeRootFile(t, root, "later.txt", "later\n")
	deliverWrites(ctx, root, "later.txt")
	closeAndSettle(t, ctx, first)
	effects := newestEffects(t, store, ctx)
	wantOutsideEffect(t, effects, "old.txt")
	if effects["later.txt"].CommandWindowID != first.ID {
		t.Fatalf("later file = %+v, want the remaining session's window", effects["later.txt"])
	}
}

func windowPins(t *testing.T, store *Store, ctx context.Context, id string) map[string]bool {
	t.Helper()
	rows, err := store.sqlDB.QueryContext(ctx, `SELECT sha256 FROM source_command_window_objects WHERE window_id = ?`, id)
	testutil.FailErr(t, "read pins", err)
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var sha string
		testutil.FailErr(t, "scan pin", rows.Scan(&sha))
		out[sha] = true
	}
	testutil.FailErr(t, "read pin rows", rows.Err())
	return out
}

// After a re-anchor the window admits against the new start, so that start's
// before-images are what it must keep.
func TestReanchorRetainsTheNewStart(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	idled := idleObserver(store)
	window, first := programWindow(t, store, ctx, root)
	first()
	testutil.Receive(t, "the window goes idle", idled)

	writeRootFile(t, root, "gap.txt", "gap\n")
	deliverWrites(ctx, root, "gap.txt")
	second, err := window.Hold(ctx)
	testutil.FailErr(t, "hold after the gap", err)
	if pins := windowPins(t, store, ctx, window.ID); !pins[sourceblob.ContentSHA([]byte("gap\n"))] {
		t.Fatalf("pins after the re-anchor = %v, want the gap's file", pins)
	}
	second()
	testutil.Receive(t, "the window goes idle again", idled)
	closeAndSettle(t, ctx, window)
}

// Without a pass between the edit and the next launch, re-anchoring is what keeps
// the gap out of the window.
func TestReanchorKeepsAGapThatNoPassObservedOutOfTheWindow(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	idled := idleObserver(store)
	window, first := programWindow(t, store, ctx, root)
	first()
	testutil.AwaitValue(t, "the window goes idle", idled)

	writeRootFile(t, root, "gap.txt", "gap\n")
	deliverWrites(ctx, root, "gap.txt")
	second, err := window.Hold(ctx)
	testutil.FailErr(t, "hold after the gap", err)
	writeRootFile(t, root, "second.txt", "second\n")
	deliverWrites(ctx, root, "second.txt")
	second()
	testutil.AwaitValue(t, "the window goes idle again", idled)
	effects := newestEffects(t, store, ctx)
	wantOutsideEffect(t, effects, "gap.txt")
	if effects["second.txt"].CommandWindowID != window.ID {
		t.Fatalf("second process's output = %+v, want the window", effects["second.txt"])
	}
	closeAndSettle(t, ctx, window)
}

// A hold whose launch gives up leaves nothing active once the anchor lands.
func TestHoldAbandonedDuringReanchorLeavesTheWindowIdle(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	idled := idleObserver(store)
	window, first := programWindow(t, store, ctx, root)
	first()
	testutil.AwaitValue(t, "the window goes idle", idled)

	gate := &gatedInventory{commandsInventoryPort: store.Commands.inventory, release: make(chan struct{}), entered: make(chan struct{}, 4)}
	store.Commands.inventory = gate
	waiting, cancel := context.WithCancel(ctx)
	failed := make(chan error, 1)
	go func() {
		_, err := window.Hold(waiting)
		failed <- err
	}()
	testutil.AwaitValue(t, "the anchor pass began", gate.entered)
	cancel()
	if err := testutil.AwaitValue(t, "the abandoned hold returns", failed); !errors.Is(err, context.Canceled) {
		t.Fatalf("abandoned hold = %v", err)
	}
	close(gate.release)
	testutil.WaitFor(t, func() bool {
		store.Commands.windowsMu.Lock()
		defer store.Commands.windowsMu.Unlock()
		return window.window.anchoring == nil
	})
	if claimant := store.Commands.attributionWindow("p1", onDiskRoots(root)); claimant != nil {
		t.Fatalf("abandoned hold left window %s claiming drift", claimant.id)
	}
	closeAndSettle(t, ctx, window)
}

func TestActiveWindowsOfTwoSessionsAttributeToNobody(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	writeRootFile(t, root, "seed.txt", "seed\n")
	first := openSessionWindow(t, store, ctx, root, "a")
	second := openSessionWindow(t, store, ctx, root, "b")
	writeRootFile(t, root, "Cargo.lock", "lock\n")
	deliverWrites(ctx, root, "Cargo.lock")
	forceTestPass(t, store, ctx, root)
	wantOutsideEffect(t, newestEffects(t, store, ctx), "Cargo.lock")
	for _, session := range []string{"a", "b"} {
		authored, err := store.Walk.SessionAuthoredPaths(ctx, "p1", session, "r1")
		testutil.FailErr(t, "session authored paths", err)
		if len(authored) != 0 {
			t.Fatalf("session %s authored %v, want nothing", session, authored)
		}
	}

	// Once one session is left it claims what comes next, not what it saw earlier.
	closeAndSettle(t, ctx, second)
	writeRootFile(t, root, "later.txt", "later\n")
	deliverWrites(ctx, root, "later.txt")
	closeAndSettle(t, ctx, first)
	effects := newestEffects(t, store, ctx)
	wantOutsideEffect(t, effects, "Cargo.lock")
	if effects["later.txt"].CommandWindowID != first.ID {
		t.Fatalf("later file = %+v, want the remaining session's window", effects["later.txt"])
	}
}

// failingInventory fails final passes until the test restores the real port.
type failingInventory struct {
	commandsInventoryPort
}

var errPassFailed = errors.New("final pass failed")

func (failingInventory) EnsureInventory(context.Context, InventoryRequest) error {
	return errPassFailed
}

// A failed window may still have drift to its name, so a peer does not claim what
// it might have been.
func TestIncompleteWindowLeavesThePeersDriftToNobody(t *testing.T) {
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	store, ctx, root := openLedgerOnDisk(t)
	writeRootFile(t, root, "seed.txt", "seed\n")
	idled := idleObserver(store)
	window, release := programWindow(t, store, ctx, root)
	peer := openSessionWindow(t, store, ctx, root, "peer")
	real := store.Commands.inventory
	store.Commands.inventory = failingInventory{real}
	release()
	testutil.Receive(t, "the grace ends", idled)
	store.Commands.inventory = real
	if listed := store.Commands.OpenSessionWindows("p1", "s1"); len(listed) != 1 || !listed[0].Incomplete {
		t.Fatalf("open windows = %+v, want it incomplete", listed)
	}
	if claimant := store.Commands.attributionWindow("p1", onDiskRoots(root)); claimant != nil {
		t.Fatalf("window %s claims with an incomplete peer in doubt", claimant.id)
	}
	writeRootFile(t, root, "Cargo.lock", "lock\n")
	forceTestPass(t, store, ctx, root)
	wantOutsideEffect(t, newestEffects(t, store, ctx), "Cargo.lock")
	closeAndSettle(t, ctx, window)
	closeAndSettle(t, ctx, peer)
}

func TestFailedSettlementReportsUnsettledAndListsAsIncomplete(t *testing.T) {
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	store, ctx, root := openLedgerOnDisk(t)
	writeRootFile(t, root, "seed.txt", "seed\n")
	window := openTestWindow(t, store, ctx, root, "make")
	writeRootFile(t, root, "gen.txt", "generated\n")
	real := store.Commands.inventory
	store.Commands.inventory = failingInventory{real}
	if err := window.Close(ctx); !errors.Is(err, errPassFailed) {
		t.Fatalf("close = %v, want the pass failure", err)
	}
	testutil.AwaitClosed(t, "settlement finishes", window.Settled())
	if window.IsSettled() {
		t.Fatal("a failed final pass reported settled")
	}
	listed := store.Commands.OpenSessionWindows("p1", "s1")
	if len(listed) != 1 || listed[0].ID != window.ID || !listed[0].Incomplete || !listed[0].Closing {
		t.Fatalf("open windows = %+v, want the incomplete window", listed)
	}
	if claimant := store.Commands.attributionWindow("p1", onDiskRoots(root)); claimant != nil {
		t.Fatalf("incomplete window %s still claims drift", claimant.id)
	}

	store.Commands.inventory = real
	forceTestPass(t, store, ctx, root)
	if listed := store.Commands.OpenSessionWindows("p1", "s1"); len(listed) != 0 {
		t.Fatalf("incomplete window survived a completed pass: %+v", listed)
	}
	if !window.IsSettled() {
		t.Fatal("window still unsettled after the pass recorded its drift")
	}
	wantOutsideEffect(t, newestEffects(t, store, ctx), "gen.txt")
}

// failingLocks refuses observation locks until the test restores the real port.
type failingLocks struct {
	commandsGitPort
}

func (failingLocks) lockObservations(context.Context, string, []RootSpec) (func(), error) {
	return nil, errPassFailed
}

func TestFailedLockLeavesTheRowUntilTheNextBoot(t *testing.T) {
	ledger := openDiskLedger(t)
	store, ctx, root := ledger.store, context.Background(), ledger.root
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	window := openTestWindow(t, store, ctx, root, "make")
	real := store.Commands.git
	store.Commands.git = failingLocks{real}
	if err := window.Close(ctx); !errors.Is(err, errPassFailed) {
		t.Fatalf("close = %v, want the lock failure", err)
	}
	if window.IsSettled() {
		t.Fatal("a failed lock reported settled")
	}
	store.Commands.git = real
	row, err := store.queries.GetSourceCommandWindow(ctx, window.ID)
	testutil.FailErr(t, "read window row", err)
	if row.State != string(api.SourceCommandWindowRunning) {
		t.Fatalf("row after failed lock = %+v, want it still running", row)
	}
	forceTestPass(t, store, ctx, root)
	if listed := store.Commands.OpenSessionWindows("p1", "s1"); len(listed) != 0 {
		t.Fatalf("a completed pass left it listed: %+v", listed)
	}
	testutil.FailErr(t, "boot recovery", New(ledger.sqlDB, ledger.contentDir).Commands.RecoverInterruptedWindows(ctx))
	if _, err := store.queries.GetSourceCommandWindow(ctx, window.ID); err == nil {
		t.Fatal("quiet window's row survived the next boot's recovery")
	}
}

func TestFailedGracePassListsAsIncompleteAndClearsOnTheNextPass(t *testing.T) {
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	store, ctx, root := openLedgerOnDisk(t)
	writeRootFile(t, root, "seed.txt", "seed\n")
	idled := idleObserver(store)
	window, release := programWindow(t, store, ctx, root)
	writeRootFile(t, root, "gen.txt", "generated\n")
	real := store.Commands.inventory
	store.Commands.inventory = failingInventory{real}
	release()
	testutil.AwaitValue(t, "the grace ends", idled)
	if listed := store.Commands.OpenSessionWindows("p1", "s1"); len(listed) != 1 || !listed[0].Incomplete || listed[0].Closing {
		t.Fatalf("open windows = %+v, want it incomplete and not closing", listed)
	}
	// Closing pays one more attempt, which fails too.
	if err := window.Close(ctx); !errors.Is(err, errPassFailed) {
		t.Fatalf("close = %v, want the pass failure", err)
	}
	if window.IsSettled() {
		t.Fatal("window settled with its drift still unrecorded")
	}
	store.Commands.inventory = real
	forceTestPass(t, store, ctx, root)
	if listed := store.Commands.OpenSessionWindows("p1", "s1"); len(listed) != 0 {
		t.Fatalf("open windows after a completed pass = %+v", listed)
	}
	wantOutsideEffect(t, newestEffects(t, store, ctx), "gen.txt")
}

// gatedInventory holds passes until the test releases them, and runs during
// inside each held pass.
type gatedInventory struct {
	commandsInventoryPort
	release  chan struct{}
	entered  chan struct{}
	during   func()
	passes   atomic.Int32
	canceled atomic.Bool
}

func (g *gatedInventory) EnsureInventory(ctx context.Context, req InventoryRequest) error {
	g.passes.Add(1)
	if g.entered != nil {
		g.entered <- struct{}{}
	}
	<-g.release
	if g.during != nil {
		g.during()
	}
	g.canceled.Store(ctx.Err() != nil)
	return g.commandsInventoryPort.EnsureInventory(ctx, req)
}

func (s *Commands) overduePassCount() int {
	s.windowsMu.Lock()
	defer s.windowsMu.Unlock()
	total := 0
	for _, count := range s.overduePasses {
		total += count
	}
	return total
}

// settableClock is the ledger's injected clock.
type settableClock struct{ now atomic.Pointer[time.Time] }

func (c *settableClock) read() time.Time { return *c.now.Load() }

func (c *settableClock) advance(d time.Duration) {
	next := c.now.Load().Add(d)
	c.now.Store(&next)
}

func manualLedgerClock(store *Store) *settableClock {
	clock := &settableClock{}
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock.now.Store(&start)
	store.Commands.now = clock.read
	return clock
}

// A forced pass that outlasts the open is reused by the next open instead of
// forcing another, so a tree without complete watcher coverage still gets windows.
func TestUntrustedCoverageConvergesOnAPassLongerThanTheBudget(t *testing.T) {
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	store, ctx, root := openLedgerOnDisk(t)
	clock := manualLedgerClock(store)
	gate := &gatedInventory{commandsInventoryPort: store.Commands.inventory, release: make(chan struct{}), entered: make(chan struct{}, 8)}
	store.Commands.inventory = gate
	in := CommandWindowInput{ProjectID: "p1", Roots: onDiskRoots(root), SessionID: "s1", ToolName: "command"}

	// Ending the open's context is the budget running out, without a short budget.
	opening, stopWaiting := context.WithCancel(ctx)
	opened := make(chan error, 1)
	go func() {
		_, err := store.Commands.OpenCommandWindow(opening, in)
		opened <- err
	}()
	testutil.AwaitValue(t, "the pass began", gate.entered)
	stopWaiting()
	if err := testutil.AwaitValue(t, "the first open gives up", opened); !errors.Is(err, context.Canceled) {
		t.Fatalf("first open = %v", err)
	}
	for range 3 {
		if window, err := store.Commands.OpenCommandWindow(ctx, in); window != nil || !errors.Is(err, ErrWindowStartPending) {
			t.Fatalf("open during the pass = %v, %v; want it refused at once", window, err)
		}
	}

	close(gate.release)
	testutil.AwaitCondition(t, "the pass finishes", func() bool { return store.Commands.overduePassCount() == 0 })
	if gate.canceled.Load() {
		t.Fatal("the open's expired budget canceled the pass")
	}
	clock.advance(slowPassReuseFloor)
	window, err := store.Commands.OpenCommandWindow(ctx, in)
	testutil.FailErr(t, "open after the pass", err)
	if passes := gate.passes.Load(); passes != 1 {
		t.Fatalf("passes after the reusing open = %d, want 1", passes)
	}
	if window.window.currentStart() == "" {
		t.Fatal("window has no known start")
	}

	// Long enough after the pass, it no longer vouches for the tree.
	clock.advance(time.Nanosecond)
	late, err := store.Commands.OpenCommandWindow(ctx, in)
	testutil.FailErr(t, "open after reuse expired", err)
	if passes := gate.passes.Load(); passes != 2 {
		t.Fatalf("passes after the stale open = %d, want a new pass", passes)
	}
	testutil.FailErr(t, "close late window", late.Close(ctx))
	testutil.FailErr(t, "close window", window.Close(ctx))
}

// A final pass longer than the budget serves the next open too, so programs
// after the first do not alternate between observed and not.
func TestFinalPassLongerThanTheBudgetServesTheNextOpen(t *testing.T) {
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	store, ctx, root := openLedgerOnDisk(t)
	clock := manualLedgerClock(store)
	first := openTestWindow(t, store, ctx, root, "make")
	released := make(chan struct{})
	close(released)
	gate := &gatedInventory{commandsInventoryPort: store.Commands.inventory, release: released, during: func() { clock.advance(store.Commands.observationBudget + time.Second) }}
	store.Commands.inventory = gate
	testutil.FailErr(t, "close first", first.Close(ctx))
	before := gate.passes.Load()
	second := openTestWindow(t, store, ctx, root, "make again")
	if passes := gate.passes.Load(); passes != before {
		t.Fatalf("passes = %d, want the open to reuse the final pass (%d)", passes, before)
	}
	testutil.FailErr(t, "close second", second.Close(ctx))
}

// Reuse lasts for the pass's own length when that is beyond the floor.
func TestSlowPassIsReusedForItsOwnLength(t *testing.T) {
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	store, ctx, root := openLedgerOnDisk(t)
	clock := manualLedgerClock(store)
	first := openTestWindow(t, store, ctx, root, "make")
	released := make(chan struct{})
	close(released)
	took := 2 * slowPassReuseFloor
	gate := &gatedInventory{commandsInventoryPort: store.Commands.inventory, release: released, during: func() { clock.advance(took) }}
	store.Commands.inventory = gate
	testutil.FailErr(t, "close first", first.Close(ctx))
	gate.during = nil

	clock.advance(slowPassReuseFloor + time.Second)
	before := gate.passes.Load()
	reused := openTestWindow(t, store, ctx, root, "within the pass's length")
	if passes := gate.passes.Load(); passes != before {
		t.Fatalf("passes = %d, want the open past the floor to reuse the pass (%d)", passes, before)
	}
	testutil.FailErr(t, "close reusing window", reused.Close(ctx))

	clock.advance(took)
	before = gate.passes.Load()
	late := openTestWindow(t, store, ctx, root, "past the pass's length")
	if passes := gate.passes.Load(); passes != before+1 {
		t.Fatalf("passes = %d, want a new one after %d", passes, before)
	}
	testutil.FailErr(t, "close late window", late.Close(ctx))
}

// Retention running out costs a file's before-image and not its attribution: a
// rewrite of an unretained file reports no captured content and cannot restore,
// and a retained one diffs exactly.
func TestPartialRetentionLosesOnlyTheBeforeImage(t *testing.T) {
	ledger := openDiskLedger(t)
	store, ctx, root := ledger.store, context.Background(), ledger.root
	// What this bounds is retention's file limit, so a loaded host must not run
	// out of time first.
	store.Commands.observationBudget, store.Commands.retention.budget = time.Hour, time.Hour
	contents := map[string]string{"a.txt": "before a\n", "b.txt": "before b\n"}
	for path, content := range contents {
		writeRootFile(t, root, path, content)
	}
	store.Commands.retention.files = 1
	partial := openTestWindow(t, store, ctx, root, "make")
	pins := windowPins(t, store, ctx, partial.ID)
	if len(pins) != 1 {
		t.Fatalf("pins with room for one = %d", len(pins))
	}
	retained, unretained := "a.txt", "b.txt"
	if !pins[sourceblob.ContentSHA([]byte(contents[retained]))] {
		retained, unretained = unretained, retained
	}

	for path := range contents {
		writeRootFile(t, root, path, "after\n")
	}
	deliverWrites(ctx, root, "a.txt", "b.txt")
	closeAndSettle(t, ctx, partial)
	testutil.FailErr(t, "reclaim unreferenced objects", store.Retention.SweepBlobs(ctx))
	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{Kind: BaselineSession, SessionID: "s1"}, 50, 0, CommitLens{})
	testutil.FailErr(t, "query walk", err)
	effects := effectsByPath(walk)

	kept, err := store.Comparisons.CompareEffect(ctx, "p1", effects[retained].ID)
	testutil.FailErr(t, "compare retained file", err)
	if effects[retained].CommandWindowID != partial.ID || kept.Before.Content != contents[retained] || kept.After.Content != "after\n" {
		t.Fatalf("retained comparison = %+v, effect = %+v", kept, effects[retained])
	}
	lost, err := store.Comparisons.CompareEffect(ctx, "p1", effects[unretained].ID)
	testutil.FailErr(t, "compare unretained file", err)
	if effects[unretained].CommandWindowID != partial.ID || lost.Before.Availability != ContentNotCaptured || lost.Before.Content != "" {
		t.Fatalf("unretained comparison = %+v, effect = %+v", lost, effects[unretained])
	}
	if _, err := store.History.ReadRestorableVersion(ctx, "p1", lost.Before.VersionID); !errors.Is(err, ErrVersionUnavailable) {
		t.Fatalf("restore of an unretained before-image = %v, want ErrVersionUnavailable", err)
	}
}

// What an earlier open stored is pinned without reading the file again, so later
// opens resume instead of paying for it twice and a file gone since still counts.
func TestRetentionPinsStoredObjectsWithoutRereadingThem(t *testing.T) {
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	store, ctx, root := openLedgerOnDisk(t)
	writeRootFile(t, root, "a.txt", "before a\n")
	writeRootFile(t, root, "b.txt", "before b\n")
	repochange.MarkCoverageCompleteForTest(root)
	t.Cleanup(func() { repochange.CloseRoot(root) })
	forceTestPass(t, store, ctx, root)
	closeAndSettle(t, ctx, openTestWindow(t, store, ctx, root, "make"))

	// With no event delivered the next open starts from the same inventory.
	testutil.FailErr(t, "remove a.txt", os.Remove(filepath.Join(root, "a.txt")))
	resumed := openTestWindow(t, store, ctx, root, "make again")
	if pins := windowPins(t, store, ctx, resumed.ID); !pins[sourceblob.ContentSHA([]byte("before a\n"))] || !pins[sourceblob.ContentSHA([]byte("before b\n"))] {
		t.Fatalf("resumed pins = %v, want both stored objects", pins)
	}
	closeAndSettle(t, ctx, resumed)
}

// Running out of time keeps the window.
func TestRetentionOutOfTimeKeepsTheWindow(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	writeRootFile(t, root, "kept.txt", "kept\n")
	store.Commands.retention.budget = 0
	starved := openTestWindow(t, store, ctx, root, "make")
	writeRootFile(t, root, "gen.txt", "generated\n")
	deliverWrites(ctx, root, "gen.txt")
	closeAndSettle(t, ctx, starved)
	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{Kind: BaselineSession, SessionID: "s1"}, 50, 0, CommitLens{})
	testutil.FailErr(t, "query session walk", err)
	if effect := effectsByPath(walk)["gen.txt"]; effect.CommandWindowID != starved.ID {
		t.Fatalf("effect = %+v, want it attributed to the starved window", effect)
	}
}

// Recovery reports its failure and can run again; until it has succeeded no window
// opens, because a later recovery would interrupt that window's live row.
func TestFailedWindowRecoveryReportsAndRetriesBeforeAnyWindowOpens(t *testing.T) {
	ledger := openDiskLedger(t)
	store, ctx, root := ledger.store, context.Background(), ledger.root
	orphan := openTestWindow(t, store, ctx, root, "npm run dev")
	writeRootFile(t, root, "public/bundle.js", "//\n")
	deliverWrites(ctx, root, "public/bundle.js")
	forceTestPass(t, store, ctx, root)

	fresh := New(ledger.sqlDB, ledger.contentDir)
	fresh.Commands.SetWindowSettleForTest(0)
	_, err := ledger.sqlDB.ExecContext(ctx, `CREATE TRIGGER refuse_recovery BEFORE UPDATE ON source_command_windows BEGIN SELECT RAISE(ABORT, 'recovery refused'); END`)
	testutil.FailErr(t, "create trigger", err)
	if err := fresh.Commands.RecoverInterruptedWindows(ctx); err == nil {
		t.Fatal("recovery reported success with its write refused")
	}
	if window, err := fresh.Commands.OpenCommandWindow(ctx, CommandWindowInput{ProjectID: "p1", Roots: onDiskRoots(root), SessionID: "s2", ToolName: "command"}); err == nil {
		t.Fatalf("window %s opened with recovery still owed", window.ID)
	}
	var fromTheOpen int
	testutil.FailErr(t, "count rows", ledger.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM source_command_windows WHERE session_id = 's2'`).Scan(&fromTheOpen))
	if fromTheOpen != 0 {
		t.Fatalf("rows from the refused open = %d", fromTheOpen)
	}
	_, err = ledger.sqlDB.ExecContext(ctx, `DROP TRIGGER refuse_recovery`)
	testutil.FailErr(t, "drop trigger", err)
	row, err := fresh.queries.GetSourceCommandWindow(ctx, orphan.ID)
	testutil.FailErr(t, "read orphan window", err)
	if row.State != string(api.SourceCommandWindowRunning) {
		t.Fatalf("orphan after failed recovery = %+v", row)
	}
	testutil.FailErr(t, "retry recovery", fresh.Commands.RecoverInterruptedWindows(ctx))
	row, err = fresh.queries.GetSourceCommandWindow(ctx, orphan.ID)
	testutil.FailErr(t, "read orphan window", err)
	if row.State != string(api.SourceCommandWindowInterrupted) {
		t.Fatalf("orphan after retry = %+v, want interrupted", row)
	}

	// A window opened after recovery survives another call: the retry is spent.
	live := openSessionWindow(t, fresh, ctx, root, "s2")
	testutil.FailErr(t, "recovery after success", fresh.Commands.RecoverInterruptedWindows(ctx))
	if row, err := fresh.queries.GetSourceCommandWindow(ctx, live.ID); err != nil || row.State != string(api.SourceCommandWindowRunning) {
		t.Fatalf("live window after a later recovery = %+v, %v", row, err)
	}
	testutil.FailErr(t, "close live window", live.Close(ctx))
}
