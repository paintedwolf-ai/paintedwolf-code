package sourceledger

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/gitstate"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWalkBeforeFirstSourceObservation(t *testing.T) {
	store, ctx := openLedger(t)
	page, err := store.QueryWalk(ctx, "p1", Baseline{Kind: BaselinePresentation}, 10, 0, CommitLens{})
	testutil.FailErr(t, "read unobserved project", err)
	if len(page.Files) != 0 || len(page.GitChanges) != 0 || page.NextBeforeOrdinal != 0 {
		t.Fatalf("unobserved project has history: %+v", page)
	}
}

func TestCommitOnlyWalkRecordsInvocationAndTurn(t *testing.T) {
	store, ctx := openLedger(t)
	testdbseed.InsertSession(t, store.sqlDB, "s1", "p1")
	insertWalkMessage(t, store, "prompt", "s1", "", "transcript", "Check in logical groups", 1)
	root := testRoots[0]
	reader := &fakeGitReader{states: map[string]gitstate.State{root.Path: {Repo: gitstate.RepoPresent, HeadCommit: "before", HeadRef: "main"}}}
	store.SetGitReader(reader)
	for i := range 13 {
		actor := Contributor{SessionID: "s1", Turn: 1, ToolCallID: fmt.Sprintf("call-%d", i), ToolName: "git_commit"}
		invocation := store.GitMutationContext(ctx, "p1", testRoots, actor)
		finish, err := gitstate.BeginMutation(invocation, root.Path, nil)
		testutil.FailErr(t, "begin commit", err)
		previous := reader.states[root.Path].HeadCommit
		next := fmt.Sprintf("commit-%d", i)
		reader.states[root.Path] = gitstate.State{Repo: gitstate.RepoPresent, HeadCommit: next, HeadRef: "main"}
		reader.logs = map[string][]gitstate.RefLogEntry{root.Path: {{Commit: next, Subject: "commit: Logical group"}, {Commit: previous, Subject: "commit: Earlier"}}}
		testutil.FailErr(t, "finish commit", finish(ctx))
	}
	page, err := store.QueryWalk(ctx, "p1", Baseline{Kind: BaselineSession, SessionID: "s1"}, 500, 0, CommitLens{})
	testutil.FailErr(t, "read commit-only walk", err)
	if len(page.Files) != 0 || len(page.GitChanges) != 13 || len(page.Turns) != 1 || page.Turns[0].MessageID != "prompt" {
		t.Fatalf("commit-only walk = %+v", page)
	}
	for _, change := range page.GitChanges {
		if change.SessionID != "s1" || change.Turn != 1 || change.ToolName != "git_commit" || change.ToolCallID == "" {
			t.Fatalf("unattributed commit: %+v", change)
		}
	}
	summary, err := store.WalkSummary(ctx, "p1", "s1", []string{"prompt"})
	testutil.FailErr(t, "read commit-only summary", err)
	if len(summary) != 1 || summary[0].Steps != 13 || summary[0].Items != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	terminal, err := store.ObserveGitState(ctx, "p1", testRoots)
	testutil.FailErr(t, "observe unchanged head", err)
	if len(terminal) != 0 {
		t.Fatalf("watcher duplicated commit: %+v", terminal)
	}
}

func seedWalkGit(t *testing.T, store *Store, id, session string, turn int, branch string, ts time.Time) {
	t.Helper()
	tx, err := store.sqlDB.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin movement", err)
	defer func() { _ = tx.Rollback() }()
	q := store.queries.WithTx(tx)
	ordinal, err := q.AdvanceSourceOrdinal(t.Context(), "p1")
	testutil.FailErr(t, "allocate movement ordinal", err)
	err = q.InsertSourceGitTransition(t.Context(), db.InsertSourceGitTransitionParams{
		ID: id, ProjectID: "p1", RootID: "r1", BranchID: branch, Kind: "commit", ToCommit: id,
		Ordinal: ordinal, ObservedTs: db.FormatTime(ts), SessionID: session, Turn: int64(turn), ToolName: "git_commit",
	})
	testutil.FailErr(t, "insert movement", err)
	testutil.FailErr(t, "commit movement", tx.Commit())
}

func TestGitOnlyWalkPagesBeyondFormerMovementCap(t *testing.T) {
	store, ctx := openLedger(t)
	testdbseed.InsertSession(t, store.sqlDB, "s1", "p1")
	for i := range 513 {
		seedWalkGit(t, store, fmt.Sprintf("g-%d", i), "s1", 1, "", time.Now())
	}
	seen := map[string]bool{}
	var before int64
	for {
		page, err := store.QueryWalk(ctx, "p1", Baseline{Kind: BaselineSession, SessionID: "s1"}, 37, before, CommitLens{})
		testutil.FailErr(t, "read movement page", err)
		if len(page.GitChanges) == 0 || len(page.GitChanges) > 37 {
			t.Fatalf("invalid page size: %d", len(page.GitChanges))
		}
		for _, change := range page.GitChanges {
			if seen[change.ID] {
				t.Fatalf("repeated movement %s", change.ID)
			}
			seen[change.ID] = true
		}
		if page.NextBeforeOrdinal == 0 {
			break
		}
		if before != 0 && page.NextBeforeOrdinal >= before {
			t.Fatal("cursor failed to advance")
		}
		before = page.NextBeforeOrdinal
	}
	if len(seen) != 513 {
		t.Fatalf("lost movements: got %d", len(seen))
	}
}

func TestWalkGitRespectsSessionTurnWorkspaceAndArchive(t *testing.T) {
	store, ctx := openLedger(t)
	testdbseed.InsertSession(t, store.sqlDB, "s1", "p1")
	testdbseed.InsertSession(t, store.sqlDB, "s2", "p1")
	now := time.Now().UTC()
	seedWalkGit(t, store, "before", "", 0, "", now.Add(-time.Hour))
	seedWalkGit(t, store, "own", "s1", 1, "", now)
	seedWalkGit(t, store, "next-turn", "s1", 2, "", now)
	seedWalkGit(t, store, "other", "s2", 1, "", now)
	seedWalkGit(t, store, "external", "", 0, "", now)
	seedWalkGit(t, store, "worktree", "s1", 1, "worktree:other", now)
	seedWalkGit(t, store, "after", "", 0, "", now.Add(time.Hour))
	_, err := store.sqlDB.ExecContext(ctx, `UPDATE sessions SET archived_at=? WHERE id='s1'`, db.FormatTime(now.Add(time.Minute)))
	testutil.FailErr(t, "archive session", err)
	for _, tc := range []struct {
		name     string
		baseline Baseline
		want     int
	}{
		{"session", Baseline{Kind: BaselineSession, SessionID: "s1"}, 2},
		{"turn", Baseline{Kind: BaselineTurn, SessionID: "s1", Turn: 1}, 1},
		{"outside", Baseline{Kind: BaselineSession, SessionID: "s1", WithOutsideChanges: true}, 3},
		{"missing", Baseline{Kind: BaselineSession, SessionID: "missing", WithOutsideChanges: true}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.baseline.RootBranches = map[string]sourcebranch.ID{"r1": ""}
			page, err := store.QueryWalk(ctx, "p1", tc.baseline, 100, 0, CommitLens{})
			testutil.FailErr(t, "read scoped movements", err)
			if len(page.GitChanges) != tc.want {
				t.Fatalf("movements = %+v", page.GitChanges)
			}
		})
	}
}

func TestGitMutationSerializesObservationAndSettlesAfterCancellation(t *testing.T) {
	store, ctx := openLedger(t)
	root := testRoots[0]
	reader := &fakeGitReader{states: map[string]gitstate.State{root.Path: {Repo: gitstate.RepoPresent, HeadCommit: "before"}}}
	store.SetGitReader(reader)
	invocation, cancel := context.WithCancel(store.GitMutationContext(ctx, "p1", testRoots, Contributor{SessionID: "s1", Turn: 1}))
	finish, err := gitstate.BeginMutation(invocation, root.Path, nil)
	testutil.FailErr(t, "begin mutation", err)
	probe, stop := context.WithTimeout(ctx, 10*time.Millisecond)
	defer stop()
	_, err = store.ObserveGitState(probe, "p1", testRoots)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("watcher consumed active mutation: %v", err)
	}
	reader.states[root.Path] = gitstate.State{Repo: gitstate.RepoPresent, HeadCommit: "after"}
	cancel()
	testutil.FailErr(t, "settle canceled mutation", finish(invocation))
	page, err := store.QueryWalk(ctx, "p1", Baseline{Kind: BaselineSession, SessionID: "s1"}, 10, 0, CommitLens{})
	testutil.FailErr(t, "read canceled effect", err)
	if len(page.GitChanges) != 1 || page.GitChanges[0].ToCommit != "after" {
		t.Fatalf("lost canceled effect: %+v", page)
	}
}

func TestCommandWindowRetainsGitOnlyHistoryAfterSettlementAndRecovery(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	reader := &fakeGitReader{states: map[string]gitstate.State{root: {Repo: gitstate.RepoPresent, HeadCommit: "before"}}}
	store.SetGitReader(reader)
	window := openTestWindow(t, store, ctx, root, "git reset --soft HEAD^")
	reader.states[root] = gitstate.State{Repo: gitstate.RepoPresent, HeadCommit: "after"}
	// No file changed and no watcher pass ran: settlement must observe HEAD.
	err := store.settleCommandWindow(ctx, window.window, time.Now(), false)
	testutil.FailErr(t, "settle command ref movement", err)
	store.recoverInterruptedWindows(ctx)
	page, err := store.QueryWalk(ctx, "p1", Baseline{Kind: BaselineSession, SessionID: "s1"}, 10, 0, CommitLens{})
	testutil.FailErr(t, "read command-only Git walk", err)
	if len(page.GitChanges) != 1 || page.GitChanges[0].CommandWindowID != window.ID || page.GitChanges[0].Turn != 3 || len(page.Commands) != 0 {
		t.Fatalf("command-only Git history = %+v", page)
	}
	row, err := store.queries.GetSourceCommandWindow(ctx, window.ID)
	testutil.FailErr(t, "read retained command", err)
	if row.State != "ended" {
		t.Fatalf("command state = %s", row.State)
	}
}

func TestGitMutationSeparatesPriorDriftAndPagesLinkedEffects(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	testdbseed.InsertSession(t, store.sqlDB, "s1", "p1")
	insertWalkMessage(t, store, "prompt", "s1", "", "transcript", "Switch branch", 1)
	trackRootFile(t, store, ctx, root, "a.txt", "original")
	reader := &fakeGitReader{states: map[string]gitstate.State{root: {Repo: gitstate.RepoPresent, HeadCommit: "original"}}}
	store.SetGitReader(reader)
	_, err := store.ObserveGitState(ctx, "p1", onDiskRoots(root))
	testutil.FailErr(t, "seed head", err)
	writeRootFile(t, root, "a.txt", "earlier outside edit")
	reader.states[root] = gitstate.State{Repo: gitstate.RepoPresent, HeadCommit: "outside"}
	invocation := store.GitMutationContext(ctx, "p1", onDiskRoots(root), Contributor{
		Origin: api.SourceChangeOriginAgent, SessionID: "s1", Turn: 1, ToolCallID: "switch", ToolName: "git_checkout",
	})
	finish, err := gitstate.BeginMutation(invocation, root, []string{"a.txt"})
	testutil.FailErr(t, "begin checkout", err)
	writeRootFile(t, root, "a.txt", "new branch")
	reader.states[root] = gitstate.State{Repo: gitstate.RepoPresent, HeadCommit: "checkout"}
	testutil.FailErr(t, "finish checkout", finish(ctx))
	baseline := Baseline{Kind: BaselineTurn, SessionID: "s1", Turn: 1}
	first, err := store.QueryWalk(ctx, "p1", baseline, 1, 0, CommitLens{})
	testutil.FailErr(t, "read file page", err)
	if len(first.Files) != 1 || len(first.GitChanges) != 1 || first.NextBeforeOrdinal == 0 {
		t.Fatalf("missing effect or reference: %+v", first)
	}
	effect := first.Files[0].Effects[0]
	movement := first.GitChanges[0]
	if effect.GitTransitionID != movement.ID || effect.Ordinal != first.NextBeforeOrdinal || effect.ToolCallID != "switch" || movement.ToCommit != "checkout" {
		t.Fatalf("wrong linked effect: %+v / %+v", effect, movement)
	}
	second, err := store.QueryWalk(ctx, "p1", baseline, 1, first.NextBeforeOrdinal, CommitLens{})
	testutil.FailErr(t, "read movement page", err)
	if len(second.Files) != 0 || len(second.GitChanges) != 1 || second.GitChanges[0].ID != movement.ID || second.NextBeforeOrdinal != 0 {
		t.Fatalf("wrong remaining page: %+v", second)
	}
	summary, err := store.WalkSummary(ctx, "p1", "s1", []string{"prompt"})
	testutil.FailErr(t, "read grouped summary", err)
	if len(summary) != 1 || summary[0].Steps != 1 || summary[0].Items != 1 {
		t.Fatalf("duplicate Git group: %+v", summary)
	}
	chain, err := store.GitTransitionChain(ctx, "p1", "r1", 10)
	testutil.FailErr(t, "read ref history", err)
	if len(chain) != 2 || chain[0].SessionID != "" || chain[1].ToolCallID != "switch" {
		t.Fatalf("misattributed history: %+v", chain)
	}
	versions, err := store.gitTransitionsForVersions(ctx, []Version{{GitTransitionID: movement.ID}})
	testutil.FailErr(t, "read version references", err)
	if versions[movement.ID].ToolCallID != "switch" {
		t.Fatalf("version lost attribution: %+v", versions)
	}
}

func TestCommandSettlementCannotDropAnActiveObservationWindow(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	window := openTestWindow(t, store, ctx, root, "git status")
	release, err := store.lockObservations(ctx, "p1", onDiskRoots(root))
	testutil.FailErr(t, "hold active observation", err)
	defer release()
	settling, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	err = store.settleCommandWindow(settling, window.window, time.Now(), false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("settlement overtook observation: %v", err)
	}
	row, err := store.queries.GetSourceCommandWindow(ctx, window.ID)
	testutil.FailErr(t, "read protected window", err)
	if row.State != "running" {
		t.Fatalf("observation window was finalized: %+v", row)
	}
	store.windowsMu.Lock()
	remaining := len(store.openWindows["p1"])
	store.windowsMu.Unlock()
	if remaining != 0 {
		t.Fatalf("failed settlement left %d windows attributing future changes", remaining)
	}
}
