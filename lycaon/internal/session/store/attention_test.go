package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/attention"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type attentionTurnStore interface {
	Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
	UpdateSession(context.Context, string, func(*api.Session)) error
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
			testutil.FailErr(t, "mark completed turn seen", st.UpdateSession(ctx, sess.ID, func(s *api.Session) { s.SeenAt = completed.CompletedAt }))
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
			testutil.FailErr(t, "read", st.UpdateSession(ctx, sess.ID, func(s *api.Session) { s.SeenAt = new(time.Now().UTC()) }))
			if got := since(); !got.Equal(failedAt) {
				t.Fatalf("record writes moved status since: %v -> %v", failedAt, got)
			}

			time.Sleep(2 * time.Millisecond)
			testutil.FailErr(t, "go busy", st.UpdateSession(ctx, sess.ID, func(s *api.Session) { s.Status = api.SessionStatusBusy }))
			if got := since(); !got.After(failedAt) {
				t.Fatalf("status change kept the old since: %v", got)
			}
		})
	}
}
