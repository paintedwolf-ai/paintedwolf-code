package sourceledger

import (
	"testing"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Two writes in one turn read as one range.
func TestCompareTurnNetsEveryWriteInTheTurn(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "load.go"},
		ProjectID:      "p1",
		Op:             api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		OperationID: "before-turn", After: []byte("one\n")})
	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "load.go"},
		ProjectID:      "p1",
		Op:             api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
		SessionID: "s1", Turn: 2, OperationID: "turn-2-first", After: []byte("two\n")})
	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "load.go"},
		ProjectID:      "p1",
		Op:             api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
		SessionID: "s1", Turn: 2, OperationID: "turn-2-second", After: []byte("three\n")})
	fileID, _ := mustResolve(t, store, ctx, "load.go")

	turn, err := store.Comparisons.CompareTurn(ctx, "p1", "s1", 2, fileID, ScopeComparisonOptions{})
	testutil.FailErr(t, "compare turn", err)
	if !turn.InRange {
		t.Fatal("turn comparison is out of range")
	}
	if turn.Before.Content != "one\n" || turn.After.Content != "three\n" {
		t.Fatalf("turn range = %q → %q, want the turn's own endpoints", turn.Before.Content, turn.After.Content)
	}
	if turn.EffectID != "" {
		t.Fatalf("composed range named effect %q, want no single effect", turn.EffectID)
	}
}

// A later turn's write does not move the range, unlike the scope comparison.
func TestCompareTurnIgnoresLaterTurns(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "load.go"},
		ProjectID:      "p1",
		Op:             api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent,
		SessionID: "s1", Turn: 1, OperationID: "turn-1", After: []byte("first\n")})
	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "load.go"},
		ProjectID:      "p1",
		Op:             api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
		SessionID: "s1", Turn: 2, OperationID: "turn-2", After: []byte("second\n")})
	fileID, _ := mustResolve(t, store, ctx, "load.go")

	turn, err := store.Comparisons.CompareTurn(ctx, "p1", "s1", 1, fileID, ScopeComparisonOptions{})
	testutil.FailErr(t, "compare turn", err)
	if turn.After.Content != "first\n" {
		t.Fatalf("turn 1 ended at %q, want what turn 1 wrote", turn.After.Content)
	}

	scope, err := store.Comparisons.CompareScope(ctx, "p1", sourcebranch.Trunk,
		Baseline{Kind: BaselineTurn, SessionID: "s1", Turn: 1}, fileID, ScopeComparisonOptions{})
	testutil.FailErr(t, "compare scope", err)
	if scope.After.Content != "second\n" {
		t.Fatalf("scope ended at %q, want the tracked head", scope.After.Content)
	}
	if scope.Before != turn.Before {
		t.Fatal("the two comparisons disagree about where the turn's range starts")
	}
}

// An untouched file answers out of range rather than failing.
func TestCompareTurnSkipsUntouchedFiles(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "other.go"},
		ProjectID:      "p1",
		Op:             api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent,
		SessionID: "s1", Turn: 1, OperationID: "turn-1", After: []byte("only\n")})
	fileID, _ := mustResolve(t, store, ctx, "other.go")

	out, err := store.Comparisons.CompareTurn(ctx, "p1", "s1", 2, fileID, ScopeComparisonOptions{})
	testutil.FailErr(t, "compare turn", err)
	if out.InRange {
		t.Fatalf("turn 2 claimed a range for a file it never touched: %+v", out)
	}
}

// A single write names its effect.
func TestCompareTurnNamesTheEffectForASingleWrite(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "once.go"},
		ProjectID:      "p1",
		Op:             api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent,
		SessionID: "s1", Turn: 4, OperationID: "turn-4", After: []byte("once\n")})
	fileID, _ := mustResolve(t, store, ctx, "once.go")

	out, err := store.Comparisons.CompareTurn(ctx, "p1", "s1", 4, fileID, ScopeComparisonOptions{})
	testutil.FailErr(t, "compare turn", err)
	if out.EffectID == "" {
		t.Fatal("a single-write range did not name its effect")
	}
	if out.Before.State != "absent" || out.After.Content != "once\n" {
		t.Fatalf("created file compared %+v → %q, want an absent pre-image", out.Before.State, out.After.Content)
	}
}
