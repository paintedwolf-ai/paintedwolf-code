package sourceledger

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/gitstate"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// fakeGitReader serves scripted positions and reflogs per root path.
type fakeGitReader struct {
	states map[string]gitstate.State
	logs   map[string][]gitstate.RefLogEntry
}

func (f *fakeGitReader) HeadState(_ context.Context, rootAbs string) gitstate.State {
	state, ok := f.states[rootAbs]
	if !ok {
		return gitstate.State{Repo: gitstate.RepoAbsent}
	}
	return state
}

func (f *fakeGitReader) RefLogHead(_ context.Context, rootAbs string, _ int) ([]gitstate.RefLogEntry, error) {
	return f.logs[rootAbs], nil
}

var testRoots = []RootSpec{{ID: "r1", Path: "/tmp/source-ledger-test"}}

func TestObserveGitStateSeedsSilentlyThenMintsTransitions(t *testing.T) {
	store, ctx := openLedger(t)
	reader := &fakeGitReader{states: map[string]gitstate.State{
		"/tmp/source-ledger-test": {Repo: gitstate.RepoPresent, HeadCommit: "aaa", HeadRef: "main"},
	}}
	store.Git.SetGitReader(reader)

	// First observation records a baseline, not an event.
	terminal, err := store.Git.ObserveGitState(ctx, "p1", testRoots)
	testutil.FailErr(t, "seed git state", err)
	if len(terminal) != 0 {
		t.Fatalf("seeding minted transitions: %+v", terminal)
	}
	transitions, err := store.Git.GitTransitionsBetween(ctx, "p1", 0, 0, 10)
	testutil.FailErr(t, "list transitions after seed", err)
	if len(transitions) != 0 {
		t.Fatalf("transitions after seed = %+v", transitions)
	}

	// A commit lands outside the app.
	reader.states["/tmp/source-ledger-test"] = gitstate.State{
		Repo: gitstate.RepoPresent, HeadCommit: "bbb", HeadRef: "main",
	}
	reader.logs = map[string][]gitstate.RefLogEntry{
		"/tmp/source-ledger-test": {
			{Commit: "bbb", Subject: "commit: Fix the bug"},
			{Commit: "aaa", Subject: "commit: earlier"},
		},
	}
	terminal, err = store.Git.ObserveGitState(ctx, "p1", testRoots)
	testutil.FailErr(t, "observe commit", err)
	if terminal["r1"] == "" {
		t.Fatalf("terminal transitions = %+v", terminal)
	}
	transitions, err = store.Git.GitTransitionsBetween(ctx, "p1", 0, 0, 10)
	testutil.FailErr(t, "list transitions", err)
	if len(transitions) != 1 {
		t.Fatalf("transitions = %+v", transitions)
	}
	got := transitions[0]
	if got.Kind != string(api.SourceGitChangeCommit) || got.FromCommit != "aaa" ||
		got.ToCommit != "bbb" || got.Detail != "Fix the bug" || got.Ordinal == 0 {
		t.Fatalf("transition = %+v", got)
	}
	if got.ID != terminal["r1"] {
		t.Fatalf("terminal id %q != recorded %q", terminal["r1"], got.ID)
	}

	// An unmoved position stays silent.
	terminal, err = store.Git.ObserveGitState(ctx, "p1", testRoots)
	testutil.FailErr(t, "re-observe", err)
	if len(terminal) != 0 {
		t.Fatalf("unmoved position minted transitions: %+v", terminal)
	}
}

func TestEffectsCarryTheirGitTransition(t *testing.T) {
	store, ctx := openLedger(t)
	reader := &fakeGitReader{states: map[string]gitstate.State{
		"/tmp/source-ledger-test": {Repo: gitstate.RepoPresent, HeadCommit: "aaa", HeadRef: "main"},
	}}
	store.Git.SetGitReader(reader)
	_, err := store.Git.ObserveGitState(ctx, "p1", testRoots)
	testutil.FailErr(t, "seed git state", err)

	reader.states["/tmp/source-ledger-test"] = gitstate.State{
		Repo: gitstate.RepoPresent, HeadCommit: "bbb", HeadRef: "work",
	}
	reader.logs = map[string][]gitstate.RefLogEntry{
		"/tmp/source-ledger-test": {
			{Commit: "bbb", Subject: "checkout: moving from main to work"},
			{Commit: "aaa", Subject: "commit: earlier"},
		},
	}
	terminal, err := store.Git.ObserveGitState(ctx, "p1", testRoots)
	testutil.FailErr(t, "observe checkout", err)

	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "swapped.txt",
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginExternal,
		After: []byte("from the other branch\n"),
		Cause: "filesystem_reconcile", CaptureQuality: "reconciled",
		GitTransitionID: terminal["r1"],
	})

	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{}, 10, 0, CommitLens{})
	testutil.FailErr(t, "query walk", err)
	if len(walk.Files) != 1 || len(walk.Files[0].Effects) != 1 {
		t.Fatalf("walk = %+v", walk.Files)
	}
	effect := walk.Files[0].Effects[0]
	if effect.GitTransitionID != terminal["r1"] {
		t.Fatalf("effect transition id = %q", effect.GitTransitionID)
	}
	// The transition is minted just below its effects on the clock, so the
	// page reports it through the reference, not the ordinal span.
	transition, ok := walkGitChangeByID(walk, effect.GitTransitionID)
	if !ok || transition.Kind != string(api.SourceGitChangeCheckout) ||
		transition.FromRef != "main" || transition.ToRef != "work" {
		t.Fatalf("walk git changes = %+v", walk.GitChanges)
	}
}

func walkGitChangeByID(walk WalkResult, id string) (GitTransition, bool) {
	for _, transition := range walk.GitChanges {
		if transition.ID == id {
			return transition, true
		}
	}
	return GitTransition{}, false
}

func TestWalkReportsBareGitTransitionsOnItsSpan(t *testing.T) {
	store, ctx := openLedger(t)
	reader := &fakeGitReader{states: map[string]gitstate.State{
		"/tmp/source-ledger-test": {Repo: gitstate.RepoPresent, HeadCommit: "aaa", HeadRef: "main"},
	}}
	store.Git.SetGitReader(reader)
	_, err := store.Git.ObserveGitState(ctx, "p1", testRoots)
	testutil.FailErr(t, "seed git state", err)

	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "notes.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		After: []byte("draft\n"),
	})

	// A commit lands with no working-tree byte change: a transition row and
	// nothing else.
	reader.states["/tmp/source-ledger-test"] = gitstate.State{
		Repo: gitstate.RepoPresent, HeadCommit: "bbb", HeadRef: "main",
	}
	reader.logs = map[string][]gitstate.RefLogEntry{
		"/tmp/source-ledger-test": {
			{Commit: "bbb", Subject: "commit: Save the draft"},
			{Commit: "aaa", Subject: "commit: earlier"},
		},
	}
	terminal, err := store.Git.ObserveGitState(ctx, "p1", testRoots)
	testutil.FailErr(t, "observe bare commit", err)

	// The first page is open above its newest effect, so the trailing bare
	// movement appears without any effect referencing it.
	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{}, 10, 0, CommitLens{})
	testutil.FailErr(t, "query walk", err)
	transition, ok := walkGitChangeByID(walk, terminal["r1"])
	if !ok || transition.Kind != string(api.SourceGitChangeCommit) ||
		transition.Detail != "Save the draft" {
		t.Fatalf("walk git changes = %+v", walk.GitChanges)
	}
	for _, file := range walk.Files {
		for _, effect := range file.Effects {
			if effect.GitTransitionID != "" {
				t.Fatalf("bare movement gained an effect reference: %+v", effect)
			}
		}
	}

	// A page with no effects anchors no span and reports nothing.
	empty, err := store.Walk.QueryWalk(ctx, "p1", Baseline{
		Kind: BaselineSession, SessionID: "no-such-session",
	}, 10, 0, CommitLens{})
	testutil.FailErr(t, "query empty walk", err)
	if len(empty.Files) != 0 || len(empty.GitChanges) != 0 {
		t.Fatalf("empty walk = %+v git changes = %+v", empty.Files, empty.GitChanges)
	}
}

func TestFileVersionsCarryTheirGitTransition(t *testing.T) {
	store, ctx := openLedger(t)
	reader := &fakeGitReader{states: map[string]gitstate.State{
		"/tmp/source-ledger-test": {Repo: gitstate.RepoPresent, HeadCommit: "aaa", HeadRef: "main"},
	}}
	store.Git.SetGitReader(reader)
	_, err := store.Git.ObserveGitState(ctx, "p1", testRoots)
	testutil.FailErr(t, "seed git state", err)

	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "swapped.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent,
		After: []byte("ours\n"),
	})

	reader.states["/tmp/source-ledger-test"] = gitstate.State{
		Repo: gitstate.RepoPresent, HeadCommit: "bbb", HeadRef: "work",
	}
	reader.logs = map[string][]gitstate.RefLogEntry{
		"/tmp/source-ledger-test": {
			{Commit: "bbb", Subject: "checkout: moving from main to work"},
			{Commit: "aaa", Subject: "commit: earlier"},
		},
	}
	terminal, err := store.Git.ObserveGitState(ctx, "p1", testRoots)
	testutil.FailErr(t, "observe checkout", err)
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "swapped.txt",
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginExternal,
		After: []byte("theirs\n"),
		Cause: "filesystem_reconcile", CaptureQuality: "reconciled",
		GitTransitionID: terminal["r1"],
	})

	fileID, _ := mustResolve(t, store, ctx, "swapped.txt")
	history, err := store.History.QueryFileVersions(ctx, "p1", fileID, 10, 0)
	testutil.FailErr(t, "query versions", err)
	var gitCaused, plain int
	for _, version := range history.Versions {
		if version.GitTransitionID == "" {
			plain++
			continue
		}
		gitCaused++
		transition, ok := history.GitTransitions[version.GitTransitionID]
		if !ok || transition.Kind != string(api.SourceGitChangeCheckout) ||
			transition.FromRef != "main" || transition.ToRef != "work" {
			t.Fatalf("version transitions = %+v", history.GitTransitions)
		}
	}
	if gitCaused != 1 || plain == 0 {
		t.Fatalf("versions = %+v", history.Versions)
	}
}

func TestReadVersionGitSourceNamesTheCommit(t *testing.T) {
	store, ctx := openLedger(t)
	reader := &fakeGitReader{states: map[string]gitstate.State{
		"/tmp/source-ledger-test": {Repo: gitstate.RepoPresent, HeadCommit: "aaa", HeadRef: "main"},
	}}
	store.Git.SetGitReader(reader)
	_, err := store.Git.ObserveGitState(ctx, "p1", testRoots)
	testutil.FailErr(t, "seed git state", err)

	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "swapped.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent,
		After: []byte("ours\n"),
	})
	reader.states["/tmp/source-ledger-test"] = gitstate.State{
		Repo: gitstate.RepoPresent, HeadCommit: "bbb", HeadRef: "work",
	}
	reader.logs = map[string][]gitstate.RefLogEntry{
		"/tmp/source-ledger-test": {
			{Commit: "bbb", Subject: "checkout: moving from main to work"},
			{Commit: "aaa", Subject: "commit: earlier"},
		},
	}
	terminal, err := store.Git.ObserveGitState(ctx, "p1", testRoots)
	testutil.FailErr(t, "observe checkout", err)
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "swapped.txt",
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginExternal,
		After: []byte("theirs\n"),
		Cause: "filesystem_reconcile", CaptureQuality: "reconciled",
		GitTransitionID: terminal["r1"],
	})

	fileID, gitVersionID := mustResolve(t, store, ctx, "swapped.txt")
	src, err := store.History.ReadVersionGitSource(ctx, "p1", gitVersionID)
	testutil.FailErr(t, "read git source", err)
	if src.Commit != "bbb" || src.RootID != "r1" || src.Path != "swapped.txt" ||
		src.State != "content" || src.FileID != fileID || src.SHA256 == "" {
		t.Fatalf("git source = %+v", src)
	}

	// The agent-authored earlier state has no git cause.
	history, err := store.History.QueryFileVersions(ctx, "p1", fileID, 10, 0)
	testutil.FailErr(t, "query versions", err)
	for _, version := range history.Versions {
		if version.GitTransitionID != "" {
			continue
		}
		if _, err := store.History.ReadVersionGitSource(ctx, "p1", version.ID); !errors.Is(err, ErrHistoryNotFound) {
			t.Fatalf("plain version git source err = %v", err)
		}
	}
}

func TestCheckpointsCarryRecordedGitPositions(t *testing.T) {
	store, ctx := openLedger(t)
	store.Git.SetGitReader(&fakeGitReader{states: map[string]gitstate.State{
		"/tmp/source-ledger-test": {Repo: gitstate.RepoPresent, HeadCommit: "aaa", HeadRef: "main"},
	}})
	_, err := store.Git.ObserveGitState(ctx, "p1", testRoots)
	testutil.FailErr(t, "seed git state", err)

	pin, err := store.Checkpoints.CreatePin(ctx, "p1", "before the rewrite")
	testutil.FailErr(t, "create pin", err)
	if len(pin.GitHeads) != 1 || pin.GitHeads[0].HeadCommit != "aaa" ||
		pin.GitHeads[0].HeadRef != "main" ||
		pin.GitHeads[0].RepoState != string(gitstate.RepoPresent) {
		t.Fatalf("pin git heads = %+v", pin.GitHeads)
	}
	page, err := store.Checkpoints.ListPinsPage(ctx, "p1", PinPageQuery{})
	testutil.FailErr(t, "list pins", err)
	if len(page.Pins) != 1 || len(page.Pins[0].GitHeads) != 1 ||
		page.Pins[0].GitHeads[0].HeadCommit != "aaa" {
		t.Fatalf("listed pins = %+v", page.Pins)
	}
}

func TestWalkHeadMatchComparesDerivedOIDs(t *testing.T) {
	store, ctx := openLedger(t)
	content := []byte("hello\n")
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "same.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		After: content,
	})
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "differs.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		After: []byte("local edit\n"),
	})
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "untracked.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		After: []byte("brand new\n"),
	})
	oids := sourceblob.ContentGitOIDs(content)
	tree := &fakeWorkingTree{treeOIDs: map[string]string{
		"/tmp/source-ledger-test/same.txt":    oids.SHA1,
		"/tmp/source-ledger-test/differs.txt": "1111111111111111111111111111111111111111",
	}}
	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{}, 10, 0, commitLensFor(tree))
	testutil.FailErr(t, "query walk", err)
	byPath := map[string]api.SourceHeadMatch{}
	for _, file := range walk.Files {
		byPath[file.Path] = file.HeadMatch
	}
	if byPath["same.txt"] != api.SourceHeadMatchSame ||
		byPath["differs.txt"] != api.SourceHeadMatchDiffers ||
		byPath["untracked.txt"] != api.SourceHeadMatchAbsent {
		t.Fatalf("head matches = %+v", byPath)
	}

	// A missing lens leaves HEAD matches unknown.
	walk, err = store.Walk.QueryWalk(ctx, "p1", Baseline{}, 10, 0, CommitLens{})
	testutil.FailErr(t, "query walk without lens", err)
	for _, file := range walk.Files {
		if file.HeadMatch != api.SourceHeadMatchUnknown {
			t.Fatalf("lensless head match = %+v", file)
		}
	}
}
