package command_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/pkg/api"
)

const writeGen = `external -- /bin/sh -c 'echo generated > gen.txt'`

// windowFixture is a program entry whose root has a real source ledger, with
// every window open counted.
type windowFixture struct {
	*programtest.Fixture
	ledger *sourceledger.Store
	opener *countingOpener
}

// countingOpener counts opens around the real ledger. before, when set, runs
// inside the open, as a reconcile pass would.
type countingOpener struct {
	inner  sourceledger.CommandWindowOpener
	opens  atomic.Int32
	before func(context.Context) error
}

func (o *countingOpener) OpenCommandWindow(ctx context.Context, in sourceledger.CommandWindowInput) (*sourceledger.OpenCommandWindow, error) {
	o.opens.Add(1)
	if o.before != nil {
		if err := o.before(ctx); err != nil {
			return nil, err
		}
	}
	return o.inner.OpenCommandWindow(ctx, in)
}

func newWindowFixture(t *testing.T) *windowFixture {
	t.Helper()
	// Without watcher coverage a close settles before the program's result.
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	f := programtest.New(t, programtest.Options{})
	ledger := f.AttachLedger(t)
	opener := &countingOpener{inner: ledger.Commands}
	configure := f.Configure
	f.Configure = func(tc *tools.ToolContext) {
		configure(tc)
		tc.Source.Commands = opener
	}
	return &windowFixture{Fixture: f, ledger: ledger, opener: opener}
}

func (f *windowFixture) run(t *testing.T, source string) command.ProgramOutcome {
	t.Helper()
	out, err := f.Call(t, t.Context(), source)
	testutil.FailErr(t, "run program", err)
	return decodeOutcome(t, out)
}

// effects reads the session's recorded effects by path.
func (f *windowFixture) effects(t *testing.T) map[string]sourceledger.Effect {
	t.Helper()
	walk, err := f.ledger.Walk.QueryWalk(t.Context(), testdbseed.DefaultProjectID, sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: f.Session}, 200, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "query session walk", err)
	effects := map[string]sourceledger.Effect{}
	for _, file := range walk.Files {
		for _, effect := range file.Effects {
			effects[effect.Path] = effect
		}
	}
	return effects
}

func (f *windowFixture) wantWindowEffect(t *testing.T, path, windowID string) {
	t.Helper()
	effect, ok := f.effects(t)[path]
	if !ok {
		t.Fatalf("no session effect for %s: %+v", path, f.effects(t))
	}
	if effect.CommandWindowID != windowID || effect.SessionID != f.Session || effect.Cause != sourceledger.CauseCommandWindow || effect.Origin != api.SourceChangeOriginExternal {
		t.Fatalf("%s effect = %+v, want window %s", path, effect, windowID)
	}
}

func (f *windowFixture) forcePass(t *testing.T, paths ...string) {
	t.Helper()
	repochange.Notify(t.Context(), repochange.Event{ProjectDir: f.Dir, Kind: repochange.WorktreeChanged, Paths: paths, Source: repochange.SourceWatcher})
	var generation int
	testutil.FailErr(t, "read roots generation", f.Database.QueryRowContext(t.Context(), `SELECT roots_generation FROM projects WHERE id = ?`, testdbseed.DefaultProjectID).Scan(&generation))
	testutil.FailErr(t, "force inventory pass", f.ledger.Inventory.EnsureInventory(t.Context(), sourceledger.InventoryRequest{
		ProjectID: testdbseed.DefaultProjectID, RootsGeneration: generation, Roots: []sourceledger.RootSpec{{ID: "r1", Path: f.Dir}}, Force: true, Wait: true,
	}))
}

func TestProgramWindowACCW01UntrustedTokenSettlesBeforeTheResult(t *testing.T) {
	f := newWindowFixture(t)
	outcome := f.run(t, writeGen)
	if outcome.Status != 0 || outcome.SourceWindow == nil || outcome.SourceWindow.ID == "" || !outcome.SourceWindow.Settled {
		t.Fatalf("outcome = %+v, want a settled source window", outcome)
	}
	f.wantWindowEffect(t, "gen.txt", outcome.SourceWindow.ID)
}

func TestProgramWindowACCW02TrustedTokenSettlesAfterTheResult(t *testing.T) {
	f := newWindowFixture(t)
	repochange.MarkCoverageCompleteForTest(f.Dir)
	t.Cleanup(func() { repochange.CloseRoot(f.Dir) })
	f.ledger.Commands.SetWindowSettleForTest(time.Hour)
	outcome := f.run(t, writeGen)
	if outcome.SourceWindow == nil || outcome.SourceWindow.Settled {
		t.Fatalf("outcome = %+v, want an unsettled source window", outcome)
	}
	listed := f.ledger.Commands.OpenSessionWindows(testdbseed.DefaultProjectID, f.Session)
	if len(listed) != 1 || listed[0].ID != outcome.SourceWindow.ID || !listed[0].Closing {
		t.Fatalf("open windows = %+v, want the program's closing window", listed)
	}
	f.forcePass(t, "gen.txt")
	<-listed[0].Settled
	if listed := f.ledger.Commands.OpenSessionWindows(testdbseed.DefaultProjectID, f.Session); len(listed) != 0 {
		t.Fatalf("settled window still listed: %+v", listed)
	}
	f.wantWindowEffect(t, "gen.txt", outcome.SourceWindow.ID)
}

// The job writes before and after the program's foreground ended.
func TestProgramWindowACCW03BackgroundJobKeepsItOpenUntilTheProgramSettles(t *testing.T) {
	f := newWindowFixture(t)
	outcome := f.run(t, `external -- /bin/sh -c 'echo job > job.txt; until test -e release; do sleep 0.01; done; echo end > after.txt' &
until [ -e job.txt ]; do sleep 0.01; done`)
	if outcome.SourceWindow == nil || outcome.SourceWindow.Settled {
		t.Fatalf("outcome = %+v, want the job's unsettled window", outcome)
	}
	listed := f.ledger.Commands.OpenSessionWindows(testdbseed.DefaultProjectID, f.Session)
	if len(listed) != 1 || listed[0].ID != outcome.SourceWindow.ID || listed[0].Closing {
		t.Fatalf("open windows = %+v, want the job's running window", listed)
	}
	testutil.FailErr(t, "release job", os.WriteFile(filepath.Join(f.Dir, "release"), nil, 0o600))
	<-listed[0].Settled
	f.wantWindowEffect(t, "job.txt", outcome.SourceWindow.ID)
	f.wantWindowEffect(t, "after.txt", outcome.SourceWindow.ID)
	if opens := f.opener.opens.Load(); opens != 1 {
		t.Fatalf("window opens = %d, want 1", opens)
	}
}

func TestProgramWindowACCW04WorkerBranchOpensNone(t *testing.T) {
	f := newWindowFixture(t)
	configure := f.Configure
	f.Configure = func(tc *tools.ToolContext) {
		configure(tc)
		tc.Source.SourceWorkspaceKind, tc.Identity.WorkerJobID = api.SourceWorkspaceKindWorker, "job-1"
		tc.Source.WorkerBranchRoot, tc.Source.BranchWorkspace = f.Dir, validBranchWorkspace{}
	}
	outcome := f.run(t, `external -- /bin/sh -c 'echo ran'`)
	if outcome.Status != 0 || outcome.SourceWindow != nil || outcome.Stdout != "ran\n" {
		t.Fatalf("worker outcome = %+v", outcome)
	}
	if opens := f.opener.opens.Load(); opens != 0 {
		t.Fatalf("worker window opens = %d, want 0", opens)
	}
	if rows := f.windowRows(t); rows != 0 {
		t.Fatalf("worker window rows = %d, want 0", rows)
	}
}

type validBranchWorkspace struct{}

func (validBranchWorkspace) ValidateMeta(context.Context) error { return nil }

func (validBranchWorkspace) EnsureParents(context.Context, string) error { return nil }

func (f *windowFixture) windowRows(t *testing.T) int {
	t.Helper()
	var rows int
	testutil.FailErr(t, "count window rows", f.Database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM source_command_windows`).Scan(&rows))
	return rows
}

func TestProgramWindowACCW05OpenFailureRunsUnobservedOnce(t *testing.T) {
	f := newWindowFixture(t)
	f.opener.before = func(context.Context) error { return errors.New("reconcile unavailable") }
	outcome := f.run(t, writeGen+`; external -- /bin/sh -c 'echo second > second.txt'`)
	if outcome.Status != 0 || outcome.SourceWindow != nil {
		t.Fatalf("outcome = %+v, want the program run with no window", outcome)
	}
	for _, name := range []string{"gen.txt", "second.txt"} {
		if _, err := os.Stat(filepath.Join(f.Dir, name)); err != nil {
			t.Fatalf("unobserved program did not run: %v", err)
		}
	}
	if opens := f.opener.opens.Load(); opens != 1 {
		t.Fatalf("window opens = %d, want one attempt", opens)
	}
}

func TestProgramWindowACCW09PipelineAndLoopShareOne(t *testing.T) {
	for name, source := range map[string]string{
		"pipeline": `external -- /bin/sh -c 'echo piped; echo a > a.txt' | external -- /bin/sh -c 'cat > b.txt'`,
		"loop":     `for i in 1 2 3 4 5 6 7 8 9 10; do external -- /bin/sh -c "echo $i > f$i.txt"; done`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newWindowFixture(t)
			outcome := f.run(t, source)
			if outcome.Status != 0 || outcome.SourceWindow == nil {
				t.Fatalf("outcome = %+v", outcome)
			}
			if opens := f.opener.opens.Load(); opens != 1 {
				t.Fatalf("window opens = %d, want 1", opens)
			}
			paths := []string{"a.txt", "b.txt"}
			if name == "loop" {
				paths = nil
				for i := 1; i <= 10; i++ {
					paths = append(paths, fmt.Sprintf("f%d.txt", i))
				}
			}
			for _, path := range paths {
				f.wantWindowEffect(t, path, outcome.SourceWindow.ID)
			}
			if rows := f.windowRows(t); rows != 1 {
				t.Fatalf("window rows = %d, want 1", rows)
			}
		})
	}
}

func TestProgramWindowIsNotOpenedForAProgramNotFound(t *testing.T) {
	f := newWindowFixture(t)
	outcome := f.run(t, `external -- paintedwolf-absent-generator`)
	if opens := f.opener.opens.Load(); opens != 0 || outcome.Status != 127 || outcome.SourceWindow != nil {
		t.Fatalf("window opens = %d, outcome = %+v", opens, outcome)
	}
}

func TestProgramWindowCoversNestedCommands(t *testing.T) {
	f := newWindowFixture(t)
	outcome := f.run(t, `timeout 60 /bin/sh -c 'echo a > timed.txt'; nohup /bin/sh -c 'echo b > nohup.txt'`)
	if outcome.Status != 0 || outcome.SourceWindow == nil {
		t.Fatalf("outcome = %+v", outcome)
	}
	if opens := f.opener.opens.Load(); opens != 1 {
		t.Fatalf("window opens = %d, want 1", opens)
	}
	f.wantWindowEffect(t, "timed.txt", outcome.SourceWindow.ID)
	f.wantWindowEffect(t, "nohup.txt", outcome.SourceWindow.ID)
}

// Every concurrent stage waits for the one open, so no process starts before
// the window's start inventory.
func TestProgramWindowOpensOnceBeforeAnyStageStarts(t *testing.T) {
	f := newWindowFixture(t)
	var startedEarly atomic.Bool
	f.opener.before = func(context.Context) error {
		entries, err := os.ReadDir(f.Dir)
		startedEarly.Store(err != nil || len(entries) != 0)
		return err
	}
	outcome := f.run(t, `external -- /bin/sh -c 'echo x; echo a > a.txt' | external -- /bin/sh -c 'cat > b.txt' | external -- /bin/sh -c 'cat > c.txt'`)
	if outcome.Status != 0 || outcome.SourceWindow == nil || startedEarly.Load() {
		t.Fatalf("outcome = %+v, started before open = %v", outcome, startedEarly.Load())
	}
	if opens := f.opener.opens.Load(); opens != 1 {
		t.Fatalf("window opens = %d, want 1", opens)
	}
}

// Drift from before the launch stays out of the program's window.
func TestProgramWindowDoesNotClaimEarlierDrift(t *testing.T) {
	f := newWindowFixture(t)
	testutil.FailErr(t, "write tracked file", os.WriteFile(filepath.Join(f.Dir, "peer.txt"), []byte("one"), 0o600))
	seed := f.run(t, `external -- /bin/sh -c 'true'`)
	if seed.SourceWindow == nil {
		t.Fatalf("seed outcome = %+v", seed)
	}
	testutil.FailErr(t, "drift before the program", os.WriteFile(filepath.Join(f.Dir, "peer.txt"), []byte("changed"), 0o600))
	outcome := f.run(t, writeGen)
	if outcome.SourceWindow == nil {
		t.Fatalf("outcome = %+v", outcome)
	}
	if peer := f.effects(t)["peer.txt"]; peer.CommandWindowID != "" || peer.SessionID != "" {
		t.Fatalf("earlier drift attributed to the program: %+v", peer)
	}
	f.wantWindowEffect(t, "gen.txt", outcome.SourceWindow.ID)
}

// A stage that gives up while the open is in flight does not cost the
// program's later launches their window.
func TestProgramWindowOpenOutlivesTheLaunchThatBeganIt(t *testing.T) {
	f := newWindowFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	f.opener.before = func(ctx context.Context) error {
		if once.CompareAndSwap(false, true) {
			close(entered)
		}
		select {
		case <-release:
			return ctx.Err()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	done := make(chan callResult, 1)
	go func() {
		content, err := f.Call(t, t.Context(), `timeout 0.01 /bin/sh -c 'echo a > canceled.txt'; echo "status=$?"; `+writeGen)
		done <- callResult{content, err}
	}()
	<-entered
	// The first launch gave up; the second now waits on the same open.
	(&programFixture{f.Fixture}).awaitExecution(t, "timeout", "completed")
	close(release)
	result := <-done
	testutil.FailErr(t, "run program", result.err)
	outcome := decodeOutcome(t, result.content)
	if outcome.SourceWindow == nil || !strings.Contains(outcome.Stdout, "status=124") {
		t.Fatalf("outcome = %+v, want a window and the first stage's timeout", outcome)
	}
	if _, err := os.Stat(filepath.Join(f.Dir, "canceled.txt")); !os.IsNotExist(err) {
		t.Fatalf("canceled stage started: %v", err)
	}
	if opens := f.opener.opens.Load(); opens != 1 {
		t.Fatalf("window opens = %d, want 1", opens)
	}
	f.wantWindowEffect(t, "gen.txt", outcome.SourceWindow.ID)
}

// A stop during the open reaches the waiting stage, and settlement abandons
// the open rather than waiting out its budget.
func TestProgramWindowStopDuringOpenAbandonsIt(t *testing.T) {
	f := newWindowFixture(t)
	entered := make(chan struct{})
	f.opener.before = func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.Call(t, t.Context(), writeGen)
		done <- err
	}()
	<-entered
	f.Runtime.Supervisor.Stop(f.Session)
	<-done
	if programs := f.Programs(t); len(programs) != 1 || programs[0] != "interrupted" {
		t.Fatalf("program state %v", programs)
	}
	if _, err := os.Stat(filepath.Join(f.Dir, "gen.txt")); !os.IsNotExist(err) {
		t.Fatalf("stopped stage started: %v", err)
	}
}

// A program that parks keeps its window across the park, and its final result
// states it.
func TestProgramWindowSpansAPark(t *testing.T) {
	f := newWindowFixture(t)
	parking := newRecordingParking()
	f.Runtime.Parking, f.Runtime.ParkThreshold = parking, time.Nanosecond
	raw, err := f.Call(t, t.Context(), writeGen+`; sleep 60`)
	testutil.FailErr(t, "park program", err)
	if !strings.Contains(raw, `"state":"parked"`) {
		t.Fatalf("parked result %s", raw)
	}
	parked := <-parking.parked
	for {
		if _, err := os.Stat(filepath.Join(f.Dir, "gen.txt")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	listed := f.ledger.Commands.OpenSessionWindows(testdbseed.DefaultProjectID, f.Session)
	if len(listed) != 1 || listed[0].Closing {
		t.Fatalf("open windows while parked = %+v", listed)
	}
	final := parked.park.Interrupt(t.Context())
	testutil.FailErr(t, "settle parked program", (<-parked.settled).err)
	outcome := decodeOutcome(t, final)
	if outcome.SourceWindow == nil || outcome.SourceWindow.ID != listed[0].ID || !outcome.SourceWindow.Settled {
		t.Fatalf("final outcome %s", final)
	}
	f.wantWindowEffect(t, "gen.txt", listed[0].ID)
}
