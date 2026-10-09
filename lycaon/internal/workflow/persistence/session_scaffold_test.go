package persistence

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSessionScaffoldRejectsWrongJSONShape(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "scaffold.db")
	sessionID := "scaffold-session"
	testdbseed.InsertSession(t, sqlDB, sessionID, testdbseed.DefaultProjectID)
	scaffold := NewSessionScaffoldSQLStore(sqlDB)
	err := scaffold.queries.UpsertSessionScaffoldVars(context.Background(), db.UpsertSessionScaffoldVarsParams{
		SessionID: sessionID,
		VarsJson:  "[]",
		UpdatedAt: db.FormatTime(time.Now().UTC()),
	})
	testutil.FailErr(t, "UpsertSessionScaffoldVars", err)
	if _, err := scaffold.GetVars(context.Background(), sessionID); err == nil {
		t.Fatal("GetVars accepted a non-object JSON value")
	}
}
