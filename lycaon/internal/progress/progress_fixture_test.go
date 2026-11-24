package progress

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
)

func newProgressFixture(t *testing.T, sessionID string) *SQLStore {
	t.Helper()
	database := testdbfixture.Open(t, "progress.db")
	testdbseed.InsertWorkflowRun(t, database, "run-"+sessionID, sessionID, testdbseed.DefaultProjectID)
	return NewSQLStore(database)
}
