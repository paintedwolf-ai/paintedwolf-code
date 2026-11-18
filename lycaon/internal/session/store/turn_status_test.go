package store

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLatestTurnStatus(t *testing.T) {
	st := openTurnSQLStore(t)
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
		reopened, err := NewSQL(st.db).LatestTurnStatus(ctx, sess.ID)
		testutil.FailErr(t, "read status from reopened store", err)
		if reopened != status {
			t.Fatalf("reopened status=%q want=%q", reopened, status)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := st.LatestTurnStatus(canceled, sess.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read=%v", err)
	}
}
