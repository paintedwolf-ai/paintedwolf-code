package authztest

import (
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func NewStore(t testing.TB, sessionIDs ...string) *authzcontext.SQLStore {
	t.Helper()
	database := testdbfixture.Open(t, "authorization.db")
	for _, sessionID := range sessionIDs {
		testdbseed.InsertSession(t, database, sessionID, testdbseed.DefaultProjectID)
	}
	return authzcontext.NewSQLStore(database)
}

func RejectEvents(t testing.TB, database db.Handle) authzcontext.LedgerRecorder {
	t.Helper()
	_, err := database.ExecContext(t.Context(), `
		CREATE TRIGGER reject_authz_event BEFORE INSERT ON authz_events
		BEGIN
			SELECT RAISE(ABORT, 'authorization event rejected by fixture');
		END
	`)
	testutil.FailErr(t, "reject authorization events", err)
	return authzcontext.LedgerRecorder{Ledger: &authzcontext.Ledger{Store: authzcontext.NewSQLStore(database)}}
}
