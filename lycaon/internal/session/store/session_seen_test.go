package store

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSQLSeenStampPublishesOnlyWhenItCrossesCompletedTurn(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "seen.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	st := NewSQL(database)
	st.SetEventOutbox(eventoutbox.New(database, events.NewMemoryHub()))
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create chat", err)
	turn, err := st.BeginTurn(ctx, TurnStart{SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: TurnOriginUser, InputJSON: "{}"})
	testutil.FailErr(t, "start turn", err)

	countUpdates := func(want int) {
		t.Helper()
		var got int
		testutil.FailErr(t, "count session updates", database.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM event_outbox WHERE session_id = ? AND topic = 'session'
			  AND json_extract(data_json, '$.action') = 'updated'
		`, sess.ID).Scan(&got))
		if got != want {
			t.Fatalf("session updates = %d, want %d", got, want)
		}
	}
	seed := sess.CreatedAt.Add(-time.Hour)
	for i := range 3 {
		testutil.FailErr(t, "stamp running chat", st.MarkSessionSeen(ctx, sess.ID, seed.Add(time.Duration(i)*time.Second)))
	}
	countUpdates(0)
	finished, err := st.FinishTurn(ctx, turn.Turn.ID, turn.Attempt.ID, TurnStatusComplete, "", "{}", "")
	testutil.FailErr(t, "finish turn", err)
	if finished.CompletedAt == nil {
		t.Fatal("completed turn has no finish time")
	}
	countUpdates(0)
	testutil.FailErr(t, "stamp just before finish", st.MarkSessionSeen(ctx, sess.ID, finished.CompletedAt.Add(-time.Nanosecond)))
	countUpdates(0)
	testutil.FailErr(t, "stamp at finish", st.MarkSessionSeen(ctx, sess.ID, *finished.CompletedAt))
	countUpdates(1)
	newest := finished.CompletedAt.Add(time.Second)
	testutil.FailErr(t, "stamp after finish", st.MarkSessionSeen(ctx, sess.ID, newest))
	testutil.FailErr(t, "retry older stamp", st.MarkSessionSeen(ctx, sess.ID, seed))
	countUpdates(1)
	got, err := st.Get(ctx, sess.ID)
	testutil.FailErr(t, "read latest stamp", err)
	if got.SeenAt == nil || !got.SeenAt.Equal(newest) {
		t.Fatalf("seen at = %v, want %v", got.SeenAt, newest)
	}
}
