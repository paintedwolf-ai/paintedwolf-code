package store

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type turnStatusStore interface {
	Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
	BeginTurn(context.Context, TurnStart) (TurnExecution, error)
	FinishTurn(context.Context, string, string, TurnStatus, string, string, string) (Turn, error)
	LatestTurnStatus(context.Context, string) (TurnStatus, error)
}

func TestLatestTurnStatusParity(t *testing.T) {
	for _, fixture := range []struct {
		name string
		open func(*testing.T) turnStatusStore
	}{
		{"memory", func(*testing.T) turnStatusStore { return NewMemory() }},
		{"sql", func(t *testing.T) turnStatusStore { return openTurnSQLStore(t).(*SQL) }},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			st := fixture.open(t)
			ctx := t.Context()
			sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			check := func(want TurnStatus) {
				t.Helper()
				got, err := st.LatestTurnStatus(ctx, sess.ID)
				testutil.FailErr(t, "read latest turn status", err)
				if got != want {
					t.Fatalf("status=%q want=%q", got, want)
				}
			}
			check("")
			for _, status := range []TurnStatus{TurnStatusComplete, TurnStatusFailed, TurnStatusInterrupted} {
				execution, err := st.BeginTurn(ctx, TurnStart{SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: TurnOriginUser, InputJSON: "{}"})
				testutil.FailErr(t, "begin turn", err)
				check(TurnStatusRunning)
				_, err = st.FinishTurn(ctx, execution.Turn.ID, execution.Attempt.ID, status, "", "{}", "")
				testutil.FailErr(t, "finish turn", err)
				check(status)
				if sql, ok := st.(*SQL); ok {
					reopened := NewSQL(sql.db)
					got, err := reopened.LatestTurnStatus(ctx, sess.ID)
					testutil.FailErr(t, "read status from reopened store", err)
					if got != status {
						t.Fatalf("reopened status=%q want=%q", got, status)
					}
				}
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if _, err := st.LatestTurnStatus(canceled, sess.ID); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled read=%v", err)
			}
		})
	}
}
