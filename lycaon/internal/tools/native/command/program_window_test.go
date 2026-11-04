package command_test

import (
	"context"
	"encoding/json"
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
	testutil.AwaitClosed(t, "window settles", listed[0].Settled)
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
	testutil.AwaitClosed(t, "window settles", listed[0].Settled)
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
	if outcome.Status != 0 || outcome.SourceWindow == nil || outcome.SourceWindow.ID != "" || !outcome.SourceWindow.Unobserved {
		t.Fatalf("outcome = %+v, want the program run with an unobserved fact and no window", outcome)
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

// A stage that skipped the wait would create its output while the open is held
// here. A slow host can only miss the mistake, never fail on correct code.
func TestProgramWindowEveryStageWaitsForTheOpen(t *testing.T) {
	f := newWindowFixture(t)
	var startedEarly atomic.Bool
	f.opener.before = func(context.Context) error {
		for range 30 {
			entries, err := os.ReadDir(f.Dir)
			if err != nil {
				return err
			}
			if len(entries) != 0 {
				startedEarly.Store(true)
				return nil
			}
			time.Sleep(10 * time.Millisecond)
		}
		return nil
	}
	outcome := f.run(t, `external -- /bin/sh -c 'echo x; echo a > a.txt' | external -- /bin/sh -c 'cat > b.txt' | external -- /bin/sh -c 'cat > c.txt'`)
	if outcome.Status != 0 || outcome.SourceWindow == nil || startedEarly.Load() {
		t.Fatalf("outcome = %+v, started before open = %v", outcome, startedEarly.Load())
	}
	if opens := f.opener.opens.Load(); opens != 1 {
		t.Fatalf("window opens = %d, want 1", opens)
	}
	for _, path := range []string{"a.txt", "b.txt", "c.txt"} {
		f.wantWindowEffect(t, path, outcome.SourceWindow.ID)
	}
}

// Drift to a file with a ledger head before the launch stays out of its window.
func TestProgramWindowDoesNotClaimEarlierDriftToATrackedFile(t *testing.T) {
	f := newWindowFixture(t)
	seed := f.run(t, `external -- /bin/sh -c 'echo one > peer.txt'`)
	if seed.SourceWindow == nil {
		t.Fatalf("seed outcome = %+v", seed)
	}
	f.wantWindowEffect(t, "peer.txt", seed.SourceWindow.ID)
	testutil.FailErr(t, "drift before the program", os.WriteFile(filepath.Join(f.Dir, "peer.txt"), []byte("changed"), 0o600))
	outcome := f.run(t, writeGen)
	if outcome.SourceWindow == nil {
		t.Fatalf("outcome = %+v", outcome)
	}
	peer, ok := f.newestEffect(t, "peer.txt")
	if !ok || peer.CommandWindowID != "" || peer.SessionID != "" || peer.Cause != sourceledger.CauseFilesystemReconcile {
		t.Fatalf("earlier drift = %+v, want a plain outside change", peer)
	}
	f.wantWindowEffect(t, "gen.txt", outcome.SourceWindow.ID)
}

// newestEffect reads a path's latest effect across every actor.
func (f *windowFixture) newestEffect(t *testing.T, path string) (sourceledger.Effect, bool) {
	t.Helper()
	walk, err := f.ledger.Walk.QueryWalk(t.Context(), testdbseed.DefaultProjectID, sourceledger.Baseline{}, 200, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "query walk", err)
	var newest sourceledger.Effect
	found := false
	for _, file := range walk.Files {
		for _, effect := range file.Effects {
			if effect.Path == path && (!found || effect.Ordinal > newest.Ordinal) {
				newest, found = effect, true
			}
		}
	}
	return newest, found
}

// durationClock reports every timer a program asks for.
type durationClock struct {
	*programtest.ManualClock
	requested chan time.Duration
}

func (c durationClock) AfterFunc(d time.Duration, fire func()) func() bool {
	c.requested <- d
	return c.ManualClock.AfterFunc(d, fire)
}

// A launch under timeout waits for the open at most half its time and then runs,
// so a slow open cannot report 124 for a process that never started. The open
// carries on for later launches, and the result says what went unobserved.
func TestTimeoutDoesNotReportExpiryForALaunchTheOpenDelayed(t *testing.T) {
	f := newWindowFixture(t)
	clock := durationClock{ManualClock: programtest.NewManualClock(), requested: make(chan time.Duration, 16)}
	f.Runtime.Clock, f.Runtime.ParkThreshold = clock, time.Hour
	release := make(chan struct{})
	f.opener.before = func(ctx context.Context) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	done := make(chan callResult, 1)
	go func() {
		content, err := f.Call(t, t.Context(), `timeout 10 /bin/sh -c 'echo a > ran.txt'; echo "status=$?"; `+writeGen)
		done <- callResult{content, err}
	}()
	for testutil.AwaitValue(t, "the launch's wait timer", clock.requested) != 5*time.Second {
	}
	clock.Advance(5 * time.Second)
	testutil.AwaitCondition(t, "the delayed launch ran", func() bool {
		_, err := os.Stat(filepath.Join(f.Dir, "ran.txt"))
		return err == nil
	})
	close(release)
	result := testutil.AwaitValue(t, "program result", done)
	testutil.FailErr(t, "run program", result.err)
	outcome := decodeOutcome(t, result.content)
	if !strings.Contains(outcome.Stdout, "status=0") {
		t.Fatalf("stdout = %q, want the launch's own status", outcome.Stdout)
	}
	if outcome.SourceWindow == nil || outcome.SourceWindow.ID == "" || !outcome.SourceWindow.Unobserved {
		t.Fatalf("source window = %+v, want the later window and the unobserved launch", outcome.SourceWindow)
	}
	if opens := f.opener.opens.Load(); opens != 1 {
		t.Fatalf("window opens = %d, want 1", opens)
	}
	f.wantWindowEffect(t, "gen.txt", outcome.SourceWindow.ID)
}

// Waiting on the program's held window keeps real cancellation distinct: a stop
// during the wait reports the interruption, not an unobserved launch.
func TestProgramResultStatesWhenObservationNeverStarted(t *testing.T) {
	f := newWindowFixture(t)
	f.opener.before = func(context.Context) error { return sourceledger.ErrWindowStartPending }
	raw, err := f.Call(t, t.Context(), writeGen)
	testutil.FailErr(t, "run program", err)
	var result struct {
		SourceWindow map[string]any `json:"source_window"`
	}
	testutil.FailErr(t, "decode result", json.Unmarshal([]byte(raw), &result))
	if len(result.SourceWindow) != 1 || result.SourceWindow["unobserved"] != true {
		t.Fatalf("source_window = %v, want only unobserved", result.SourceWindow)
	}
}

const waitForRelease = `touch started; until test -e release; do sleep 0.01; done`

// Every way a program can keep an external process live holds the window: drift
// while it runs is the program's, and drift after it ends is not.
func TestProgramWindowIsActiveWhileAnyLaunchIsLive(t *testing.T) {
	for name, source := range map[string]string{
		"job":        `external -- /bin/sh -c '` + waitForRelease + `' & external -- /bin/sh -c 'true'; wait`,
		"pipeline":   `external -- /bin/sh -c '` + waitForRelease + `' | external -- /bin/sh -c 'cat >/dev/null'`,
		"xargs":      `echo go | xargs -n1 /bin/sh -c '` + waitForRelease + `'`,
		"env":        `env MARK=1 /bin/sh -c '` + waitForRelease + `'`,
		"timeout":    `timeout 3600 /bin/sh -c '` + waitForRelease + `'`,
		"nohup":      `nohup /bin/sh -c '` + waitForRelease + `'`,
		"substitute": `echo "$(/bin/sh -c '` + waitForRelease + `')"`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newWindowFixture(t)
			f.Runtime.ParkThreshold = time.Hour
			idled := make(chan string, 4)
			f.ledger.Commands.SetWindowSettleForTest(0)
			f.ledger.Commands.SetWindowIdledForTest(func(id string) { idled <- id })
			done := make(chan callResult, 1)
			go func() {
				content, err := f.Call(t, t.Context(), source+`; touch finished; until [ -e end ]; do sleep 0.01; done`)
				done <- callResult{content, err}
			}()
			testutil.AwaitCondition(t, "the live launch started", func() bool {
				_, err := os.Stat(filepath.Join(f.Dir, "started"))
				return err == nil
			})
			testutil.FailErr(t, "drift during the launch", os.WriteFile(filepath.Join(f.Dir, "during.txt"), []byte("live"), 0o600))
			f.forcePass(t, "during.txt")
			listed := f.ledger.Commands.OpenSessionWindows(testdbseed.DefaultProjectID, f.Session)
			if len(listed) != 1 {
				t.Fatalf("open windows = %+v", listed)
			}
			f.wantWindowEffect(t, "during.txt", listed[0].ID)

			testutil.FailErr(t, "end the launch", os.WriteFile(filepath.Join(f.Dir, "release"), nil, 0o600))
			testutil.AwaitValue(t, "the window goes idle", idled)
			testutil.FailErr(t, "drift after the launch", os.WriteFile(filepath.Join(f.Dir, "after.txt"), []byte("gap"), 0o600))
			f.forcePass(t, "after.txt")
			after, ok := f.newestEffect(t, "after.txt")
			if !ok || after.CommandWindowID != "" || after.SessionID != "" {
				t.Fatalf("drift after the last launch = %+v, want a plain outside change", after)
			}
			testutil.FailErr(t, "end the program", os.WriteFile(filepath.Join(f.Dir, "end"), nil, 0o600))
			result := testutil.AwaitValue(t, "program result", done)
			testutil.FailErr(t, "run program", result.err)
		})
	}
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
	testutil.AwaitClosed(t, "the open began", entered)
	f.Runtime.Supervisor.Stop(f.Session)
	testutil.AwaitValue(t, "the stopped program returns", done)
	if programs := f.Programs(t); len(programs) != 1 || programs[0] != "interrupted" {
		t.Fatalf("program state %v", programs)
	}
	if _, err := os.Stat(filepath.Join(f.Dir, "gen.txt")); !os.IsNotExist(err) {
		t.Fatalf("stopped stage started: %v", err)
	}
}

// A job's first native statement releases the foreground, so the result can
// return before the job's later launch finishes opening the window. The result
// cannot state it yet; the job's changes are still attributed to it.
func TestProgramWindowOpenInFlightAtTheResultStillObservesTheJob(t *testing.T) {
	f := newWindowFixture(t)
	release := make(chan struct{})
	f.opener.before = func(ctx context.Context) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	outcome := f.run(t, `{ sleep 0.01; `+writeGen+`; } &`)
	if outcome.SourceWindow != nil {
		t.Fatalf("outcome = %+v, want no window before the open finished", outcome)
	}
	close(release)
	testutil.AwaitCondition(t, "the job ran and its window settled", func() bool {
		_, err := os.Stat(filepath.Join(f.Dir, "gen.txt"))
		return err == nil && f.opener.opens.Load() == 1 && len(f.ledger.Commands.OpenSessionWindows(testdbseed.DefaultProjectID, f.Session)) == 0
	})
	effect, ok := f.effects(t)["gen.txt"]
	if !ok || effect.CommandWindowID == "" || effect.SessionID != f.Session {
		t.Fatalf("job effect = %+v", effect)
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
	parked := testutil.AwaitValue(t, "the program parks", parking.parked)
	testutil.AwaitCondition(t, "the generator ran", func() bool {
		_, err := os.Stat(filepath.Join(f.Dir, "gen.txt"))
		return err == nil
	})
	listed := f.ledger.Commands.OpenSessionWindows(testdbseed.DefaultProjectID, f.Session)
	if len(listed) != 1 || listed[0].Closing {
		t.Fatalf("open windows while parked = %+v", listed)
	}
	final := parked.park.Interrupt(t.Context())
	testutil.FailErr(t, "settle parked program", testutil.AwaitValue(t, "parked program settles", parked.settled).err)
	outcome := decodeOutcome(t, final)
	if outcome.SourceWindow == nil || outcome.SourceWindow.ID != listed[0].ID || !outcome.SourceWindow.Settled {
		t.Fatalf("final outcome %s", final)
	}
	f.wantWindowEffect(t, "gen.txt", listed[0].ID)
}

// A person's edit while a program is parked after its last external process is
// no part of that program's window, whether the file had a ledger head or not.
func TestOutsideEditDuringAParkIsNotAttributedToTheProgram(t *testing.T) {
	f := newWindowFixture(t)
	f.ledger.Commands.SetWindowSettleForTest(0)
	idled := make(chan string, 2)
	f.ledger.Commands.SetWindowIdledForTest(func(id string) { idled <- id })
	parking := newRecordingParking()
	f.Runtime.Parking, f.Runtime.ParkThreshold = parking, time.Nanosecond
	raw, err := f.Call(t, t.Context(), writeGen+`; sleep 60`)
	testutil.FailErr(t, "park program", err)
	if !strings.Contains(raw, `"state":"parked"`) {
		t.Fatalf("parked result %s", raw)
	}
	parked := testutil.AwaitValue(t, "the program parks", parking.parked)
	windowID := testutil.AwaitValue(t, "the window goes idle after the generator", idled)
	f.wantWindowEffect(t, "gen.txt", windowID)

	for path, content := range map[string]string{"notes.md": "a person's note\n", "gen.txt": "a person's edit\n"} {
		testutil.FailErr(t, "outside edit", os.WriteFile(filepath.Join(f.Dir, path), []byte(content), 0o600))
	}
	f.forcePass(t, "notes.md", "gen.txt")

	final := parked.park.Interrupt(t.Context())
	testutil.FailErr(t, "settle parked program", testutil.AwaitValue(t, "parked program settles", parked.settled).err)
	outcome := decodeOutcome(t, final)
	if outcome.SourceWindow == nil || outcome.SourceWindow.ID != windowID || !outcome.SourceWindow.Settled {
		t.Fatalf("final outcome %s", final)
	}
	for _, path := range []string{"notes.md", "gen.txt"} {
		effect, ok := f.newestEffect(t, path)
		if !ok || effect.CommandWindowID != "" || effect.SessionID != "" || effect.Origin != api.SourceChangeOriginExternal {
			t.Fatalf("%s effect = %+v, want a plain outside change", path, effect)
		}
	}
}

// A process that starts after a gap brings the window back without taking the gap.
func TestLaunchAfterAGapDoesNotClaimTheGap(t *testing.T) {
	f := newWindowFixture(t)
	f.Runtime.ParkThreshold = time.Hour
	f.ledger.Commands.SetWindowSettleForTest(0)
	idled := make(chan string, 2)
	f.ledger.Commands.SetWindowIdledForTest(func(id string) { idled <- id })
	done := make(chan callResult, 1)
	go func() {
		content, err := f.Call(t, t.Context(), writeGen+`; until [ -e go ]; do sleep 0.01; done; external -- /bin/sh -c 'echo second > second.txt'`)
		done <- callResult{content, err}
	}()
	windowID := testutil.AwaitValue(t, "the window goes idle", idled)
	testutil.FailErr(t, "write during the gap", os.WriteFile(filepath.Join(f.Dir, "gap.txt"), []byte("gap"), 0o600))
	testutil.FailErr(t, "continue the program", os.WriteFile(filepath.Join(f.Dir, "go"), nil, 0o600))
	result := testutil.AwaitValue(t, "program result", done)
	testutil.FailErr(t, "run program", result.err)
	outcome := decodeOutcome(t, result.content)
	if outcome.SourceWindow == nil || outcome.SourceWindow.ID != windowID || outcome.SourceWindow.Unobserved {
		t.Fatalf("outcome = %+v", outcome)
	}
	f.wantWindowEffect(t, "second.txt", windowID)
	if gap, ok := f.newestEffect(t, "gap.txt"); !ok || gap.CommandWindowID != "" || gap.SessionID != "" {
		t.Fatalf("gap effect = %+v, want a plain outside change", gap)
	}
	if opens := f.opener.opens.Load(); opens != 1 {
		t.Fatalf("window opens = %d, want the one window re-anchored", opens)
	}
}
