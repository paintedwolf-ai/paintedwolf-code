package sourceledger

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWalkSummaryPreservesTurnsAndGroupedSteps(t *testing.T) {
	store, ctx := openLedger(t)
	testdbseed.InsertSession(t, store.sqlDB, "s1", "p1")
	insertWalkMessage(t, store, "first", "s1", "", "transcript", "Request", 1)
	insertWalkMessage(t, store, "internal", "s1", "", "internal", "Host", 2)
	insertWalkMessage(t, store, "continuation", "s1", "user_continuation", "transcript", "More", 3)
	insertWalkMessage(t, store, "second", "s1", "", "transcript", "Next", 4)
	insertWalkMessage(t, store, "empty", "s1", "", "transcript", "No changes", 5)
	_, err := store.sqlDB.ExecContext(ctx, `INSERT INTO source_command_windows
 (id,project_id,session_id,turn,tool_name,command_line,state,ordinal,started_ts)
 VALUES ('cmd','p1','s1',1,'command','fixture','ended',1,'2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "seed command window", err)
	for i, id := range []string{"git", "mixed"} {
		_, err := store.sqlDB.ExecContext(ctx, `INSERT INTO source_git_transitions
 (id,project_id,root_id,kind,ordinal,observed_ts) VALUES (?,'p1','r1','checkout',?,'2026-01-01T00:00:00Z')`, id, i+1)
		testutil.FailErr(t, "seed git movement", err)
	}
	for i, entry := range []struct {
		turn               int
		path, command, git string
	}{
		{1, "a", "", ""}, {1, "a", "", ""},
		{1, "b", "cmd", ""}, {2, "c", "cmd", ""},
		{1, "d", "cmd", "git"}, {1, "e", "", "git"},
		{1, "f", "", "mixed"}, {2, "g", "", "mixed"},
		{2, "h", "", ""},
	} {
		mustRecord(t, store, ctx, RecordInput{ProjectID: "p1", RootID: "r1", SessionID: "s1", Turn: entry.turn,
			Path: entry.path, OperationID: fmt.Sprintf("op-%d", i), CommandWindowID: entry.command, GitTransitionID: entry.git,
			Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent, Before: []byte(fmt.Sprint(i)), After: []byte(fmt.Sprint(i + 1)),
		})
	}
	summaries, err := store.Walk.WalkSummary(ctx, "p1", "s1", []string{"first", "second", "empty", "internal", "continuation"})
	testutil.FailErr(t, "read turn counts", err)
	// The turn each message opened travels with its counts, so a card can address
	// the same turn the turn baseline names.
	want := []api.SourceWalkTurnSummary{
		{MessageID: "first", Turn: 1, Steps: 4, Items: 5},
		{MessageID: "second", Turn: 2, Steps: 1, Items: 1},
		{MessageID: "empty", Turn: 3},
	}
	if fmt.Sprint(summaries) != fmt.Sprint(want) {
		t.Fatalf("summaries = %+v, want %+v", summaries, want)
	}
	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{Kind: BaselineSession, SessionID: "s1"}, 500, 0, CommitLens{})
	testutil.FailErr(t, "read full walk", err)
	effects := 0
	for _, file := range walk.Files {
		effects += len(file.Effects)
	}
	if effects != 9 || len(walk.Commands) != 1 || len(walk.GitChanges) != 2 {
		t.Fatalf("full Walk lost history: effects=%d commands=%d git=%d", effects, len(walk.Commands), len(walk.GitChanges))
	}
	if _, err := store.Walk.WalkSummary(ctx, "p1", "s1", make([]string, 101)); err == nil {
		t.Fatal("unbounded preview request accepted")
	}
	foreign, err := store.Walk.WalkSummary(ctx, "p2", "s1", []string{"first"})
	testutil.FailErr(t, "read other project", err)
	if len(foreign) != 0 {
		t.Fatalf("cross-project preview: %+v", foreign)
	}
}
