package draftstest

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/workflow/drafts"
)

func NewStore(t testing.TB, sessionIDs ...string) *drafts.SQL {
	t.Helper()
	database := testdbfixture.Open(t, "workflow-drafts.db")
	for _, sessionID := range sessionIDs {
		testdbseed.InsertSession(t, database, sessionID, testdbseed.DefaultProjectID)
	}
	return drafts.NewSQL(database)
}
