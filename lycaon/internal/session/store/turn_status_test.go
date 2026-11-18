package store

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLatestTurnStatusParity(t *testing.T) {
	for _, fixture := range []struct {
		name string
		open func(t *testing.T) *Repositories
	}{
		{"sql", func(t *testing.T) *Repositories { return openTurnSQLStore(t) }},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			st := fixture.open(t)
			ctx := t.Context()
			sess, err := st.Sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			check := func(want TurnStatus) {
				t.Helper()
				got, err := st.Turns.LatestTurnStatus(ctx, sess.ID)
				testutil.FailErr(t, "read latest turn status", err)
				if got != want {
					t.Fatalf("status=%q want=%q", got, want)
				}
			}
			check("")
			for _, status := range []TurnStatus{TurnStatusComplete, TurnStatusFailed, TurnStatusInterrupted} {
				execution, err := st.Turns.BeginTurn(ctx, TurnStart{SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: TurnOriginUser, InputJSON: "{}"})
				testutil.FailErr(t, "begin turn", err)
				check(TurnStatusRunning)
				_, err = st.Turns.FinishTurn(ctx, execution.Turn.ID, execution.Attempt.ID, status, "", "{}", "")
				testutil.FailErr(t, "finish turn", err)
				check(status)
				{
					sql := st
					reopened := NewSQL(sql.core.db)
					got, err := reopened.Turns.LatestTurnStatus(ctx, sess.ID)
					testutil.FailErr(t, "read status from reopened store", err)
					if got != status {
						t.Fatalf("reopened status=%q want=%q", got, status)
					}
				}
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if _, err := st.Turns.LatestTurnStatus(canceled, sess.ID); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled read=%v", err)
			}
		})
	}
}
