package store

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func appendUserPrompt(t *testing.T, st *Repositories, sessionID, text string) string {
	t.Helper()
	id := uuid.NewString()
	testutil.FailErr(t, "append "+text, st.Transcript.AppendMessages(t.Context(), sessionID, api.Message{
		ID: id, Role: api.MessageRoleUser, Content: text, Origin: api.MessageOriginUser, CreatedAt: time.Now().UTC(),
	}))
	return id
}

func TestTurnClockRecordsOnlyOnceItsOpeningMessageExists(t *testing.T) {

	ctx := t.Context()
	st := newSQLTestStore(t)
	sess, err := st.Sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	since := time.Date(2026, time.September, 12, 9, 0, 0, 0, time.UTC)
	pending := TurnClock{SessionID: sess.ID, OpeningMessageID: uuid.NewString(), RunningAt: &since}
	testutil.FailErr(t, "put clock before its message", st.Turns.PutTurnClock(ctx, pending))
	if _, ok, err := st.Turns.LatestTurnClock(ctx, sess.ID); err != nil || ok {
		t.Fatalf("latest clock before the opening message = (%v, %v), want none", ok, err)
	}

	first := appendUserPrompt(t, st, sess.ID, "first")
	second := appendUserPrompt(t, st, sess.ID, "second")
	settled := since.Add(5 * time.Minute)
	testutil.FailErr(t, "put first clock", st.Turns.PutTurnClock(ctx, TurnClock{
		SessionID: sess.ID, OpeningMessageID: first, ActiveMs: 300000, WorkMs: 240000, SettledAt: &settled,
	}))
	testutil.FailErr(t, "put second clock", st.Turns.PutTurnClock(ctx, TurnClock{
		SessionID: sess.ID, OpeningMessageID: second, ActiveMs: 1000, WorkMs: 5000, RunningAt: &settled,
	}))

	latest, ok, err := st.Turns.LatestTurnClock(ctx, sess.ID)
	testutil.FailErr(t, "latest clock", err)
	if !ok || latest.OpeningMessageID != second || latest.WorkMs != 1000 || latest.RunningAt == nil {
		t.Fatalf("latest clock = %+v, want the running second turn with work capped at active", latest)
	}

	rewindFrom(t, st, sess.ID, second)
	latest, ok, err = st.Turns.LatestTurnClock(ctx, sess.ID)
	testutil.FailErr(t, "latest clock after rewind", err)
	if !ok || latest.OpeningMessageID != first {
		t.Fatalf("latest clock after rewind = %+v, want the first turn", latest)
	}

}

func TestAbandonedTurnClockSettlesAtLastTurnActivity(t *testing.T) {

	ctx := t.Context()
	st := newSQLTestStore(t)
	sess, err := st.Sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	opening := appendUserPrompt(t, st, sess.ID, "run")
	since := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	testutil.FailErr(t, "put running clock", st.Turns.PutTurnClock(ctx, TurnClock{
		SessionID: sess.ID, OpeningMessageID: opening, ActiveMs: 2000, WorkMs: 1000, RunningAt: &since,
	}))
	_, err = st.Turns.BeginTurn(ctx, TurnStart{
		ID: uuid.NewString(), SessionID: sess.ID, ProjectID: sess.ProjectID,
		Origin: TurnOriginUser, InputJSON: `{"text":"run"}`,
	})
	testutil.FailErr(t, "begin turn", err)

	settled, err := st.Turns.SettleAbandonedTurnClocks(ctx)
	after := time.Now().UTC().Add(time.Second)
	testutil.FailErr(t, "settle abandoned clocks", err)
	if settled != 1 {
		t.Fatalf("settled = %d, want 1", settled)
	}
	clock, ok, err := st.Turns.LatestTurnClock(ctx, sess.ID)
	testutil.FailErr(t, "latest clock", err)
	if !ok || clock.RunningAt != nil || clock.SettledAt == nil {
		t.Fatalf("recovered clock = %+v, want paused and settled", clock)
	}
	if clock.SettledAt.Before(since) || clock.SettledAt.After(after) {
		t.Fatalf("settled at %s, want within [%s, %s]", clock.SettledAt, since, after)
	}
	elapsed := clock.SettledAt.Sub(since).Milliseconds()
	if clock.ActiveMs != 2000+elapsed || clock.WorkMs != 1000+elapsed {
		t.Fatalf("recovered clock = %+v, want %d ms of running time added", clock, elapsed)
	}
	if again, err := st.Turns.SettleAbandonedTurnClocks(ctx); err != nil || again != 0 {
		t.Fatalf("second recovery settled %d (%v), want none", again, err)
	}

}
