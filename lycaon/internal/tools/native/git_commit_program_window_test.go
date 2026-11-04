package native

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/pkg/api"
)

// A generated file stages with the session's work because the session's own
// program was observed creating it; a peer's dirty file stays out.
func TestGitCommitStagesWhatTheSessionsProgramGeneratedACCW10(t *testing.T) {
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	f := programtest.New(t, programtest.Options{})
	ledger := f.AttachLedger(t)
	manager := git.NewManager()
	ledger.Git.SetGitReader(walkGitReader{manager})
	gittest.Init(t, f.Dir)
	testutil.FailErr(t, "write tracked file", os.WriteFile(filepath.Join(f.Dir, "README"), []byte("seed\n"), 0o644))
	gittest.CommitAll(t, f.Dir, "Initial")
	testutil.FailErr(t, "write peer file", os.WriteFile(filepath.Join(f.Dir, "peer.txt"), []byte("peer\n"), 0o644))

	raw, err := f.Call(t, t.Context(), `external -- /bin/sh -c 'echo generated > gen.txt'`)
	testutil.FailErr(t, "run generator", err)
	var outcome struct {
		Status       int `json:"status"`
		SourceWindow *struct {
			ID string `json:"id"`
		} `json:"source_window"`
	}
	testutil.FailErr(t, "decode program outcome", json.Unmarshal([]byte(raw), &outcome))
	if outcome.Status != 0 || outcome.SourceWindow == nil {
		t.Fatalf("generator outcome %s", raw)
	}
	walk, err := ledger.Walk.QueryWalk(t.Context(), testdbseed.DefaultProjectID, sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: f.Session}, 10, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "read session walk", err)
	var generated *sourceledger.Effect
	for _, file := range walk.Files {
		for i := range file.Effects {
			if file.Effects[i].Path == "gen.txt" {
				generated = &file.Effects[i]
			}
		}
	}
	if generated == nil || generated.SessionID != f.Session || generated.Cause != sourceledger.CauseCommandWindow || generated.CommandWindowID != outcome.SourceWindow.ID || generated.Origin != api.SourceChangeOriginExternal {
		t.Fatalf("generated effect = %+v", generated)
	}

	tc := nativefixture.Context(f.Dir)
	tc.Identity.ProjectID, tc.Identity.SessionID, tc.Identity.ToolCallID = testdbseed.DefaultProjectID, f.Session, "commit-call"
	tc.Invocation.ToolName = "git_commit"
	tc.Source.SourceLedger = ledger
	tc.Source.History = tools.SourceHistory{Files: ledger.History, Comparison: ledger.Comparisons, Git: ledger.Git, Authorship: ledger.Walk}
	tc.Source.Commands, tc.Source.Observations, tc.Source.GitMutations = ledger.Commands, ledger.Inventory, ledger.Git
	tool := &GitCommitTool{Git: manager, Boundary: nativefixture.Boundary(t)}
	_, err = tool.Run(t.Context(), map[string]any{"message": "Generate"}, tc)
	testutil.FailErr(t, "commit session work", err)
	committed := strings.Fields(gittest.Run(t, f.Dir, "show", "--name-only", "--format=", "HEAD"))
	if !slices.Contains(committed, "gen.txt") || slices.Contains(committed, "peer.txt") {
		t.Fatalf("committed paths = %v, want gen.txt without peer.txt", committed)
	}
}

// A person's save while a program sits between external processes is not staged
// by the session's bulk commit; explicit paths remain the way to commit it.
func TestBulkCommitLeavesAnOutsideEditMadeBetweenProcessesOfAProgram(t *testing.T) {
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	f := programtest.New(t, programtest.Options{})
	f.Runtime.ParkThreshold = time.Hour
	ledger := f.AttachLedger(t)
	ledger.Commands.SetWindowSettleForTest(0)
	idled := make(chan string, 2)
	ledger.Commands.SetWindowIdledForTest(func(id string) { idled <- id })
	manager := git.NewManager()
	ledger.Git.SetGitReader(walkGitReader{manager})
	seedRepository(t, f.Dir)

	done := make(chan error, 1)
	go func() {
		_, err := f.Call(t, t.Context(), `external -- /bin/sh -c 'echo generated > gen.txt'; until [ -e done.flag ]; do sleep 0.01; done`)
		done <- err
	}()
	testutil.AwaitValue(t, "the window goes idle", idled)
	testutil.FailErr(t, "outside edit", os.WriteFile(filepath.Join(f.Dir, "notes.md"), []byte("a person's note\n"), 0o644))
	testutil.FailErr(t, "pass during the gap", ledger.Inventory.EnsureInventory(t.Context(), sourceledger.InventoryRequest{
		ProjectID: testdbseed.DefaultProjectID, Roots: []sourceledger.RootSpec{{ID: "r1", Path: f.Dir}}, Force: true, Wait: true,
	}))
	testutil.FailErr(t, "end the program", os.WriteFile(filepath.Join(f.Dir, "done.flag"), nil, 0o644))
	testutil.FailErr(t, "run program", testutil.AwaitValue(t, "program result", done))

	_, err := commitTool(manager, t).Run(t.Context(), map[string]any{"message": "Generate"}, commitContext(f, ledger, f.Session))
	testutil.FailErr(t, "commit session work", err)
	committed := strings.Fields(gittest.Run(t, f.Dir, "show", "--name-only", "--format=", "HEAD"))
	if !slices.Contains(committed, "gen.txt") || slices.Contains(committed, "notes.md") {
		t.Fatalf("committed paths = %v, want gen.txt without notes.md", committed)
	}
}

// Drift while two sessions' windows are active belongs to neither bulk commit.
func TestBulkCommitLeavesDriftObservedWhileTwoSessionsWereActive(t *testing.T) {
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	f := programtest.New(t, programtest.Options{})
	ledger := f.AttachLedger(t)
	manager := git.NewManager()
	ledger.Git.SetGitReader(walkGitReader{manager})
	seedRepository(t, f.Dir)
	_, err := f.Call(t, t.Context(), `external -- /bin/sh -c 'echo generated > gen.txt'`)
	testutil.FailErr(t, "run generator", err)

	roots := []sourceledger.RootSpec{{ID: "r1", Path: f.Dir}}
	open := func(session string) *sourceledger.OpenCommandWindow {
		window, err := ledger.Commands.OpenCommandWindow(t.Context(), sourceledger.CommandWindowInput{
			ProjectID: testdbseed.DefaultProjectID, Roots: roots, SessionID: session, ToolName: "command",
		})
		testutil.FailErr(t, "open window for "+session, err)
		return window
	}
	mine, peer := open(f.Session), open("peer-session")
	testutil.FailErr(t, "write lockfile", os.WriteFile(filepath.Join(f.Dir, "Cargo.lock"), []byte("lock\n"), 0o644))
	testutil.FailErr(t, "close my window", mine.Close(t.Context()))
	testutil.FailErr(t, "close peer window", peer.Close(t.Context()))

	_, err = commitTool(manager, t).Run(t.Context(), map[string]any{"message": "Generate"}, commitContext(f, ledger, f.Session))
	testutil.FailErr(t, "commit session work", err)
	committed := strings.Fields(gittest.Run(t, f.Dir, "show", "--name-only", "--format=", "HEAD"))
	if !slices.Contains(committed, "gen.txt") || slices.Contains(committed, "Cargo.lock") {
		t.Fatalf("committed paths = %v, want gen.txt without Cargo.lock", committed)
	}
	_, err = commitTool(manager, t).Run(t.Context(), map[string]any{"message": "Peer"}, commitContext(f, ledger, "peer-session"))
	if rejection := toolrejection.AsToolReject(err); rejection == nil || rejection.Code != "GIT_COMMIT_NO_SESSION_AUTHORSHIP" {
		t.Fatalf("peer bulk commit = %v, want no authorship", err)
	}
	_, err = commitTool(manager, t).Run(t.Context(), map[string]any{"message": "Lockfile", "paths": []any{"Cargo.lock"}}, commitContext(f, ledger, f.Session))
	testutil.FailErr(t, "commit the lockfile by explicit path", err)
	if committed := gittest.Run(t, f.Dir, "show", "--name-only", "--format=", "HEAD"); !strings.Contains(committed, "Cargo.lock") {
		t.Fatalf("explicit commit = %q", committed)
	}
}

// A person's edit to a file a window claimed earlier is theirs: it stays out of
// the bulk commit and uncommitted_paths reports it.
func TestBulkCommitLeavesAPersonsEditToAFileAWindowClaimed(t *testing.T) {
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	f := programtest.New(t, programtest.Options{})
	ledger := f.AttachLedger(t)
	manager := git.NewManager()
	ledger.Git.SetGitReader(walkGitReader{manager})
	seedRepository(t, f.Dir)
	_, err := f.Call(t, t.Context(), `external -- /bin/sh -c 'echo generated > gen.txt; echo generated > kept.txt'`)
	testutil.FailErr(t, "run generator", err)

	testutil.FailErr(t, "outside edit", os.WriteFile(filepath.Join(f.Dir, "gen.txt"), []byte("a person's edit\n"), 0o644))
	testutil.FailErr(t, "pass after the edit", ledger.Inventory.EnsureInventory(t.Context(), sourceledger.InventoryRequest{
		ProjectID: testdbseed.DefaultProjectID, Roots: []sourceledger.RootSpec{{ID: "r1", Path: f.Dir}}, Force: true, Wait: true,
	}))

	raw, err := commitTool(manager, t).Run(t.Context(), map[string]any{"message": "Generate"}, commitContext(f, ledger, f.Session))
	testutil.FailErr(t, "commit session work", err)
	committed := strings.Fields(gittest.Run(t, f.Dir, "show", "--name-only", "--format=", "HEAD"))
	if !slices.Contains(committed, "kept.txt") || slices.Contains(committed, "gen.txt") {
		t.Fatalf("committed paths = %v, want kept.txt without gen.txt", committed)
	}
	var receipt struct {
		UncommittedPaths []string `json:"uncommitted_paths"`
	}
	testutil.FailErr(t, "decode receipt", json.Unmarshal([]byte(raw), &receipt))
	if !slices.Contains(receipt.UncommittedPaths, "gen.txt") {
		t.Fatalf("uncommitted paths = %v, want gen.txt", receipt.UncommittedPaths)
	}
}

// windowsThenGate lists the session's windows and ends their settle period only
// once a caller has asked, so what the caller read next depends on its waiting.
type windowsThenGate struct {
	tools.SourceWindowReader
	gate chan time.Time
	once sync.Once
}

func (w *windowsThenGate) OpenSessionWindows(projectID, sessionID string) []sourceledger.SessionWindow {
	listed := w.SourceWindowReader.OpenSessionWindows(projectID, sessionID)
	w.once.Do(func() { close(w.gate) })
	return listed
}

// runTrustedGenerator runs a generator on trusted coverage, whose result returns
// before the window settles, and delivers the watcher's event for it.
func runTrustedGenerator(t *testing.T, f *programtest.Fixture, name string) {
	t.Helper()
	_, err := f.Call(t, t.Context(), `external -- /bin/sh -c 'echo generated > `+name+`'`)
	testutil.FailErr(t, "run generator", err)
	repochange.Notify(t.Context(), repochange.Event{ProjectDir: f.Dir, Kind: repochange.WorktreeChanged, Paths: []string{name}, Source: repochange.SourceWatcher})
}

// A commit in the next call includes the build's output though the window is
// still settling.
func TestBulkCommitWaitsForItsSessionsClosingWindow(t *testing.T) {
	f := programtest.New(t, programtest.Options{})
	ledger := f.AttachLedger(t)
	manager := git.NewManager()
	ledger.Git.SetGitReader(walkGitReader{manager})
	seedRepository(t, f.Dir)
	repochange.MarkCoverageCompleteForTest(f.Dir)
	t.Cleanup(func() { repochange.CloseRoot(f.Dir) })
	gate := make(chan time.Time)
	ledger.Commands.SetWindowSettleGateForTest(gate)
	runTrustedGenerator(t, f, "gen.txt")

	tc := commitContext(f, ledger, f.Session)
	tc.Source.History.Windows = &windowsThenGate{SourceWindowReader: ledger.Commands, gate: gate}
	_, err := commitTool(manager, t).Run(t.Context(), map[string]any{"message": "Generate"}, tc)
	testutil.FailErr(t, "commit session work", err)
	if committed := strings.Fields(gittest.Run(t, f.Dir, "show", "--name-only", "--format=", "HEAD")); !slices.Contains(committed, "gen.txt") {
		t.Fatalf("committed paths = %v, want gen.txt", committed)
	}
}

func seedRepository(t *testing.T, dir string) {
	t.Helper()
	gittest.Init(t, dir)
	testutil.FailErr(t, "write tracked file", os.WriteFile(filepath.Join(dir, "README"), []byte("seed\n"), 0o644))
	gittest.CommitAll(t, dir, "Initial")
}

func commitTool(manager *git.Manager, t *testing.T) *GitCommitTool {
	t.Helper()
	return &GitCommitTool{Git: manager, Boundary: nativefixture.Boundary(t)}
}

func commitContext(f *programtest.Fixture, ledger *sourceledger.Store, session string) tools.ToolContext {
	tc := nativefixture.Context(f.Dir)
	tc.Identity.ProjectID, tc.Identity.SessionID, tc.Identity.ToolCallID = testdbseed.DefaultProjectID, session, "commit-call"
	tc.Invocation.ToolName = "git_commit"
	tc.Source.SourceLedger = ledger
	tc.Source.History = tools.SourceHistory{Files: ledger.History, Comparison: ledger.Comparisons, Git: ledger.Git, Authorship: ledger.Walk}
	tc.Source.Commands, tc.Source.Observations, tc.Source.GitMutations = ledger.Commands, ledger.Inventory, ledger.Git
	return tc
}
