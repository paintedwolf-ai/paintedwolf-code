package store

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/lycaon/lycaon/internal/attention"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type attentionTurnStore interface {
	Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
	UpdateSession(context.Context, string, func(*api.Session)) error
	MarkSessionSeen(context.Context, string, time.Time) error
	BeginTurn(context.Context, TurnStart) (TurnExecution, error)
	FinishTurn(context.Context, string, string, TurnStatus, string, string, string) (Turn, error)
	LatestFinishes(context.Context) (map[string]time.Time, error)
	ListAttentionCandidates(context.Context) ([]attention.Candidate, error)
}

func TestAttentionCompletedTurnParity(t *testing.T) {
	for _, fixture := range []struct {
		name string
		open func(*testing.T) attentionTurnStore
	}{
		{"memory", func(*testing.T) attentionTurnStore { return NewMemory() }},
		{"sql", func(t *testing.T) attentionTurnStore { return openTurnSQLStore(t).(*SQL) }},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			st := fixture.open(t)
			ctx := t.Context()
			sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create attention session", err)
			finish := func(status TurnStatus) Turn {
				t.Helper()
				opened, err := st.BeginTurn(ctx, TurnStart{SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: TurnOriginUser, InputJSON: "{}"})
				testutil.FailErr(t, "begin attention turn", err)
				turn, err := st.FinishTurn(ctx, opened.Turn.ID, opened.Attempt.ID, status, "", "{}", "")
				testutil.FailErr(t, "finish attention turn", err)
				return turn
			}
			completed := finish(TurnStatusComplete)
			finish(TurnStatusFailed)
			finishes, err := st.LatestFinishes(ctx)
			testutil.FailErr(t, "read latest completion", err)
			if completed.CompletedAt == nil || !finishes[sess.ID].Equal(*completed.CompletedAt) {
				t.Fatalf("latest finishes = %v, completed turn = %+v", finishes, completed)
			}
			candidates, err := st.ListAttentionCandidates(ctx)
			testutil.FailErr(t, "read unread candidate", err)
			if len(candidates) != 1 || candidates[0].SessionID != sess.ID {
				t.Fatalf("unread candidates = %+v", candidates)
			}
			testutil.FailErr(t, "mark completed turn seen", st.MarkSessionSeen(ctx, sess.ID, *completed.CompletedAt))
			candidates, err = st.ListAttentionCandidates(ctx)
			testutil.FailErr(t, "read seen candidates", err)
			if len(candidates) != 0 {
				t.Fatalf("seen turn still needs attention: %+v", candidates)
			}
			testutil.FailErr(t, "mark session busy", st.UpdateSession(ctx, sess.ID, func(s *api.Session) { s.Status = api.SessionStatusBusy }))
			candidates, err = st.ListAttentionCandidates(ctx)
			testutil.FailErr(t, "read active candidate", err)
			if len(candidates) != 1 || candidates[0].SeenAt == nil || !candidates[0].SeenAt.Equal(*completed.CompletedAt) {
				t.Fatalf("active candidate lost seen time: %+v", candidates)
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if _, err := st.LatestFinishes(canceled); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled latest finishes: %v", err)
			}
		})
	}
}

// A candidate's status age starts when it entered its status. Renames and
// reads rewrite the record without moving it; a session still in the status it
// was created with dates from its creation.
func TestAttentionCandidateStatusSinceParity(t *testing.T) {
	for _, fixture := range []struct {
		name string
		open func(*testing.T) attentionTurnStore
	}{
		{"memory", func(*testing.T) attentionTurnStore { return NewMemory() }},
		{"sql", func(t *testing.T) attentionTurnStore { return openTurnSQLStore(t).(*SQL) }},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				st := fixture.open(t)
				ctx := t.Context()
				sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
				testutil.FailErr(t, "create session", err)
				since := func() time.Time {
					t.Helper()
					candidates, err := st.ListAttentionCandidates(ctx)
					testutil.FailErr(t, "list candidates", err)
					if len(candidates) != 1 {
						t.Fatalf("candidates = %+v", candidates)
					}
					return candidates[0].StatusSince
				}

				time.Sleep(2 * time.Millisecond)
				testutil.FailErr(t, "fail session", st.UpdateSession(ctx, sess.ID, func(s *api.Session) { s.Status = api.SessionStatusError }))
				failedAt := since()
				if !failedAt.After(sess.CreatedAt) {
					t.Fatalf("status since %v, want after creation %v", failedAt, sess.CreatedAt)
				}

				time.Sleep(2 * time.Millisecond)
				testutil.FailErr(t, "rename", st.UpdateSession(ctx, sess.ID, func(s *api.Session) { s.Title = "Renamed" }))
				testutil.FailErr(t, "read", st.MarkSessionSeen(ctx, sess.ID, time.Now()))
				if got := since(); !got.Equal(failedAt) {
					t.Fatalf("record writes moved status since: %v -> %v", failedAt, got)
				}

				time.Sleep(2 * time.Millisecond)
				testutil.FailErr(t, "go busy", st.UpdateSession(ctx, sess.ID, func(s *api.Session) { s.Status = api.SessionStatusBusy }))
				if got := since(); !got.After(failedAt) {
					t.Fatalf("status change kept the old since: %v", got)
				}
			})
		})
	}
}

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
	latestTurn, err := st.BeginTurn(ctx, TurnStart{SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: TurnOriginUser, InputJSON: "{}"})
	testutil.FailErr(t, "start another turn", err)
	latest, err := st.FinishTurn(ctx, latestTurn.Turn.ID, latestTurn.Attempt.ID, TurnStatusComplete, "", "{}", "")
	testutil.FailErr(t, "finish another turn", err)
	if latest.CompletedAt == nil {
		t.Fatal("latest turn has no finish time")
	}
	testutil.FailErr(t, "stamp at earlier finish", st.MarkSessionSeen(ctx, sess.ID, *finished.CompletedAt))
	countUpdates(0)
	testutil.FailErr(t, "stamp at latest finish", st.MarkSessionSeen(ctx, sess.ID, *latest.CompletedAt))
	countUpdates(1)
	newest := latest.CompletedAt.Add(time.Second)
	testutil.FailErr(t, "stamp after finish", st.MarkSessionSeen(ctx, sess.ID, newest))
	testutil.FailErr(t, "retry older stamp", st.MarkSessionSeen(ctx, sess.ID, seed))
	countUpdates(1)
	got, err := st.Get(ctx, sess.ID)
	testutil.FailErr(t, "read latest stamp", err)
	if got.SeenAt == nil || !got.SeenAt.Equal(newest) {
		t.Fatalf("seen at = %v, want %v", got.SeenAt, newest)
	}
}
