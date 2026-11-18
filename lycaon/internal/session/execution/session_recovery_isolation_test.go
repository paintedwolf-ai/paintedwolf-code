package execution_test

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLiveSessionRecoveryLeavesOtherTurnsBusy(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "turns.db")
	for _, id := range []string{"interrupted", "running"} {
		testdbseed.InsertSession(t, database, id, testdbseed.DefaultProjectID)
	}
	_, err := database.ExecContext(ctx, "UPDATE sessions SET status = 'busy'")
	testutil.FailErr(t, "mark live turns", err)
	manager := session.NewHost(store.NewSQL(database), session.Models{Limits: settings.DefaultSessionLimits()}, nil)
	recovery := manager.Stops.Recovery
	assertStatus := func(id string, want api.SessionStatus) {
		t.Helper()
		var got api.SessionStatus
		testutil.FailErr(t, "read persisted session", database.QueryRowContext(ctx, "SELECT status FROM sessions WHERE id = ?", id).Scan(&got))
		if got != want {
			t.Fatalf("%s status = %s, want %s", id, got, want)
		}
	}
	if err := recovery.RecoverSession(ctx, "missing"); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("recover missing session: %v", err)
	}
	for range 2 {
		testutil.FailErr(t, "recover interrupted turn", recovery.RecoverSession(ctx, "interrupted"))
		assertStatus("interrupted", api.SessionStatusIdle)
		assertStatus("running", api.SessionStatusBusy)
	}
	testutil.FailErr(t, "recover remaining turns on restart", recovery.RecoverOrphanedTurns(ctx))
	assertStatus("interrupted", api.SessionStatusIdle)
	assertStatus("running", api.SessionStatusIdle)
}
