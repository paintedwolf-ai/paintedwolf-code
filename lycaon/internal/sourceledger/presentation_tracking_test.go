package sourceledger

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// lookFixture drives one file, a.go, through writes and looks.
type lookFixture struct {
	t     *testing.T
	store *Store
	ctx   context.Context
}

func newLookFixture(t *testing.T) lookFixture {
	store, ctx := openLedger(t)
	return lookFixture{t: t, store: store, ctx: ctx}
}

func (f lookFixture) write(origin api.SourceChangeOrigin, op api.SourceChangeOp, text string) (string, int64) {
	f.t.Helper()
	mustRecord(f.t, f.store, f.ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "a.go"},
		ProjectID:      "p1", Op: op, Origin: origin, After: []byte(text)})
	effectID := latestEffectID(f.t, f.store, f.ctx)
	row, err := f.store.queries.GetSourceEffect(f.ctx, effectID)
	testutil.FailErr(f.t, "read effect", err)
	return effectID, row.Ordinal
}

func (f lookFixture) fileID() string {
	f.t.Helper()
	fileID, _ := mustResolve(f.t, f.store, f.ctx, "a.go")
	return fileID
}

func (f lookFixture) look(effectID string, ordinal int64) {
	f.t.Helper()
	testutil.FailErr(f.t, "complete presentation",
		f.store.Checkpoints.CompletePresentation(f.ctx, "p1", f.fileID(), effectID, ordinal))
}

func (f lookFixture) newEffects() []string {
	f.t.Helper()
	res, err := f.store.Walk.QueryWalk(f.ctx, "p1", Baseline{Kind: BaselinePresentation}, 100, 0, CommitLens{})
	testutil.FailErr(f.t, "query new files", err)
	var ids []string
	for _, file := range res.Files {
		for _, effect := range file.Effects {
			ids = append(ids, effect.ID)
		}
	}
	return ids
}

func (f lookFixture) seen(withoutUserEdits bool) []SeenFile {
	f.t.Helper()
	res, err := f.store.History.QuerySeen(f.ctx, "p1", nil, SeenPageQuery{Limit: DefaultSeenFiles}, withoutUserEdits)
	testutil.FailErr(f.t, "query seen files", err)
	return res.Files
}

func (f lookFixture) compare(unmarkUserEdits bool) Comparison {
	f.t.Helper()
	out, err := f.store.Comparisons.CompareScope(f.ctx, "p1", sourcebranch.Trunk,
		Baseline{Kind: BaselinePresentation}, f.fileID(),
		ScopeComparisonOptions{UnmarkUserEdits: unmarkUserEdits})
	testutil.FailErr(f.t, "compare scope", err)
	return out
}

func (f lookFixture) pendingAgentEffects() int {
	f.t.Helper()
	var n int
	testutil.FailErr(f.t, "count pending agent effects", f.store.sqlDB.QueryRowContext(f.ctx,
		`SELECT count(*) FROM source_agent_presentations WHERE file_id = ?`, f.fileID()).Scan(&n))
	return n
}

func effectIDs(effects []Effect) []string {
	out := make([]string, 0, len(effects))
	for _, effect := range effects {
		out = append(out, effect.ID)
	}
	return out
}

func sameIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestLookCoversChangesFromOutsideTheApp(t *testing.T) {
	f := newLookFixture(t)
	outside, ordinal := f.write(api.SourceChangeOriginExternal, api.SourceChangeOpCreate, "one\n")

	res, err := f.store.Walk.QueryWalk(f.ctx, "p1", Baseline{Kind: BaselinePresentation}, 100, 0, CommitLens{})
	testutil.FailErr(t, "query new files", err)
	if len(res.Files) != 1 || res.Files[0].PresentationEffectID != outside {
		t.Fatalf("new files = %+v, want a.go with token %s", res.Files, outside)
	}

	f.look(outside, ordinal)
	if got := f.newEffects(); len(got) != 0 {
		t.Fatalf("new after the look = %v, want none", got)
	}
	seen := f.seen(false)
	if len(seen) != 1 || seen[0].ThroughOrdinal != ordinal || !sameIDs(effectIDs(seen[0].Effects), []string{outside}) {
		t.Fatalf("seen = %+v, want a.go through %d covering %s", seen, ordinal, outside)
	}
}

func TestUnreadComparisonStartsAfterTheAcknowledgedVersion(t *testing.T) {
	f := newLookFixture(t)
	first, firstOrdinal := f.write(api.SourceChangeOriginAgent, api.SourceChangeOpCreate, "one\n")
	f.look(first, firstOrdinal)
	second, secondOrdinal := f.write(api.SourceChangeOriginAgent, api.SourceChangeOpWrite, "one\ntwo\n")

	if got := f.newEffects(); !sameIDs(got, []string{second}) {
		t.Fatalf("new = %v, want only %s", got, second)
	}
	if seen := f.seen(false); len(seen) != 0 {
		t.Fatalf("seen while new work waits = %+v, want none", seen)
	}
	mixed := f.compare(false)
	if mixed.Before.Content != "one\n" || mixed.After.Content != "one\ntwo\n" {
		t.Fatalf("unread comparison = %+v, want one → one two", mixed)
	}

	f.look(second, secondOrdinal)
	seen := f.seen(false)
	if len(seen) != 1 || seen[0].ThroughOrdinal != secondOrdinal || !sameIDs(effectIDs(seen[0].Effects), []string{second}) {
		t.Fatalf("seen = %+v, want the latest look covering only %s", seen, second)
	}
	settled := f.compare(false)
	if settled.InRange {
		t.Fatalf("reviewed file has an unread comparison: %+v", settled)
	}
	reviewed, err := f.store.Comparisons.CompareReviewed(f.ctx, "p1", f.fileID(), secondOrdinal)
	testutil.FailErr(t, "load reviewed range", err)
	if reviewed.Before.Content != "one\n" || reviewed.After.Content != "one\ntwo\n" {
		t.Fatalf("reviewed range = %+v", reviewed)
	}
}

func TestAnEarlierOrRepeatedLookChangesNothing(t *testing.T) {
	f := newLookFixture(t)
	first, firstOrdinal := f.write(api.SourceChangeOriginAgent, api.SourceChangeOpCreate, "one\n")
	second, secondOrdinal := f.write(api.SourceChangeOriginAgent, api.SourceChangeOpWrite, "two\n")
	f.look(second, secondOrdinal)
	f.look(second, secondOrdinal)
	f.look(first, firstOrdinal)

	seen := f.seen(false)
	if len(seen) != 1 || seen[0].ThroughOrdinal != secondOrdinal ||
		!sameIDs(effectIDs(seen[0].Effects), []string{second, first}) {
		t.Fatalf("seen = %+v, want one look through %d covering both writes", seen, secondOrdinal)
	}
}

func TestLookAtAnotherFileIsAMismatch(t *testing.T) {
	f := newLookFixture(t)
	effectID, ordinal := f.write(api.SourceChangeOriginAgent, api.SourceChangeOpCreate, "one\n")
	err := f.store.Checkpoints.CompletePresentation(f.ctx, "p1", f.fileID(), effectID, ordinal+1)
	if !errors.Is(err, ErrPresentationMismatch) {
		t.Fatalf("wrong ordinal = %v, want ErrPresentationMismatch", err)
	}
	err = f.store.Checkpoints.CompletePresentation(f.ctx, "p1", f.fileID(), "no-such-effect", ordinal)
	if !errors.Is(err, ErrPresentationMismatch) {
		t.Fatalf("unknown effect = %v, want ErrPresentationMismatch", err)
	}
}

func TestMarkUnseenReturnsTheLatestLookAndItsAgentWork(t *testing.T) {
	f := newLookFixture(t)
	first, firstOrdinal := f.write(api.SourceChangeOriginAgent, api.SourceChangeOpCreate, "one\n")
	f.look(first, firstOrdinal)
	second, secondOrdinal := f.write(api.SourceChangeOriginAgent, api.SourceChangeOpWrite, "one\ntwo\n")
	f.look(second, secondOrdinal)
	if n := f.pendingAgentEffects(); n != 0 {
		t.Fatalf("pending after both looks = %d, want 0", n)
	}

	if err := f.store.Checkpoints.WithdrawPresentation(f.ctx, "p1", f.fileID(), firstOrdinal); !errors.Is(err, ErrPresentationMismatch) {
		t.Fatalf("withdraw an older look = %v, want ErrPresentationMismatch", err)
	}
	testutil.FailErr(t, "withdraw latest look", f.store.Checkpoints.WithdrawPresentation(f.ctx, "p1", f.fileID(), secondOrdinal))

	if got := f.newEffects(); !sameIDs(got, []string{second}) {
		t.Fatalf("new after withdrawing = %v, want %s", got, second)
	}
	if n := f.pendingAgentEffects(); n != 1 {
		t.Fatalf("pending after withdrawing = %d, want the second write back", n)
	}
	comparison := f.compare(false)
	if comparison.Before.Content != "one\n" {
		t.Fatalf("comparison after withdrawing = %+v, want one as the baseline", comparison)
	}
	if err := f.store.Checkpoints.WithdrawPresentation(f.ctx, "p1", f.fileID(), firstOrdinal); !errors.Is(err, ErrPresentationNotFound) {
		t.Fatalf("withdraw an empty look = %v, want ErrPresentationNotFound", err)
	}
}

func TestMarkUnseenAfterTheFirstLookForgetsTheFile(t *testing.T) {
	f := newLookFixture(t)
	effectID, ordinal := f.write(api.SourceChangeOriginAgent, api.SourceChangeOpCreate, "one\n")
	f.look(effectID, ordinal)
	testutil.FailErr(t, "withdraw", f.store.Checkpoints.WithdrawPresentation(f.ctx, "p1", f.fileID(), ordinal))

	if got := f.newEffects(); !sameIDs(got, []string{effectID}) {
		t.Fatalf("new = %v, want %s", got, effectID)
	}
	if n := f.pendingAgentEffects(); n != 1 {
		t.Fatalf("pending = %d, want 1", n)
	}
	if err := f.store.Checkpoints.WithdrawPresentation(f.ctx, "p1", f.fileID(), ordinal); !errors.Is(err, ErrPresentationNotFound) {
		t.Fatalf("withdraw again = %v, want ErrPresentationNotFound", err)
	}
}

func TestUnmarkedOwnEditsKeepAFileSeen(t *testing.T) {
	f := newLookFixture(t)
	agent, ordinal := f.write(api.SourceChangeOriginAgent, api.SourceChangeOpCreate, "one\n")
	f.look(agent, ordinal)
	f.write(api.SourceChangeOriginUser, api.SourceChangeOpWrite, "one\nmine\n")

	if seen := f.seen(false); len(seen) != 0 {
		t.Fatalf("seen with own edits marked = %+v, want none", seen)
	}
	seen := f.seen(true)
	if len(seen) != 1 || !sameIDs(effectIDs(seen[0].Effects), []string{agent}) {
		t.Fatalf("seen with own edits unmarked = %+v, want a.go covering %s", seen, agent)
	}
	comparison := f.compare(true)
	if !comparison.UserEditsUnmarked || comparison.Before.Content != "one\n" || comparison.After.Content != "one\nmine\n" {
		t.Fatalf("comparison = %+v, want own edits unmarked after the acknowledged baseline", comparison)
	}
}

func TestReviewedComparisonRemainsExactWhenNewWorkArrives(t *testing.T) {
	f := newLookFixture(t)
	first, firstOrdinal := f.write(api.SourceChangeOriginAgent, api.SourceChangeOpCreate, "one\n")
	f.look(first, firstOrdinal)
	second, secondOrdinal := f.write(api.SourceChangeOriginAgent, api.SourceChangeOpWrite, "two\n")
	f.look(second, secondOrdinal)
	third, thirdOrdinal := f.write(api.SourceChangeOriginAgent, api.SourceChangeOpWrite, "three\n")
	reviewed, err := f.store.Comparisons.CompareReviewed(f.ctx, "p1", f.fileID(), secondOrdinal)
	testutil.FailErr(t, "read historical review", err)
	if reviewed.Before.Content != "one\n" || reviewed.After.Content != "two\n" {
		t.Fatalf("reviewed range followed live head: %+v", reviewed)
	}
	if got := f.newEffects(); !sameIDs(got, []string{third}) {
		t.Fatalf("historical read changed unread effects: %v", got)
	}
	f.look(third, thirdOrdinal)
	_, err = f.store.Comparisons.CompareReviewed(f.ctx, "p1", f.fileID(), secondOrdinal)
	if !errors.Is(err, ErrPresentationMismatch) {
		t.Fatalf("superseded look error = %v", err)
	}
	_, err = f.store.Comparisons.CompareReviewed(f.ctx, "other", f.fileID(), thirdOrdinal)
	if !errors.Is(err, ErrPresentationNotFound) {
		t.Fatalf("cross-project look error = %v", err)
	}
	testutil.FailErr(t, "withdraw latest look", f.store.Checkpoints.WithdrawPresentation(f.ctx, "p1", f.fileID(), thirdOrdinal))
	_, err = f.store.Comparisons.CompareReviewed(f.ctx, "p1", f.fileID(), thirdOrdinal)
	if !errors.Is(err, ErrPresentationMismatch) {
		t.Fatalf("withdrawn look error = %v", err)
	}
}

func TestOpenPresentationRetainsItsUnreadBoundary(t *testing.T) {
	f := newLookFixture(t)
	first, firstOrdinal := f.write(api.SourceChangeOriginAgent, api.SourceChangeOpCreate, "one\n")
	f.look(first, firstOrdinal)
	second, secondOrdinal := f.write(api.SourceChangeOriginAgent, api.SourceChangeOpWrite, "two\n")
	opened := f.compare(false)
	if opened.PresentationAfterOrdinal == nil || *opened.PresentationAfterOrdinal != firstOrdinal {
		t.Fatalf("open comparison omitted its boundary: %+v", opened)
	}
	f.look(second, secondOrdinal)
	f.write(api.SourceChangeOriginAgent, api.SourceChangeOpWrite, "three\n")
	held, err := f.store.Comparisons.CompareScope(f.ctx, "p1", sourcebranch.Trunk,
		Baseline{Kind: BaselinePresentation}, f.fileID(), ScopeComparisonOptions{
			PresentationAfterOrdinal: opened.PresentationAfterOrdinal,
		})
	testutil.FailErr(t, "compare held presentation", err)
	if held.Before.Content != "one\n" || held.After.Content != "three\n" {
		t.Fatalf("open view changed its start: %+v", held)
	}
	if current := f.compare(false); current.Before.Content != "two\n" {
		t.Fatalf("reopened view did not use the current acknowledgment: %+v", current)
	}
}

func TestPresentationRefusesAnEffectOutsideVisibleHistory(t *testing.T) {
	f := newLookFixture(t)
	effectID, ordinal := f.write(api.SourceChangeOriginAgent, api.SourceChangeOpCreate, "one\n")
	_, err := f.store.sqlDB.ExecContext(f.ctx, "UPDATE source_effects SET walk_visible = 0 WHERE id = ?", effectID)
	testutil.FailErr(t, "hide internal effect", err)
	if err := f.store.Checkpoints.CompletePresentation(f.ctx, "p1", f.fileID(), effectID, ordinal); !errors.Is(err, ErrPresentationMismatch) {
		t.Fatalf("internal effect acknowledgment = %v", err)
	}
}
