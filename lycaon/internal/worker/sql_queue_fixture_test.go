package worker

import (
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"testing"
)

func newTestQueue(t testing.TB, maxRunning int, projectID string, sessionIDs ...string) *SQLQueue {
	t.Helper()
	database := testdbfixture.Open(t, "workers.db")
	testdbseed.InsertProject(t, database, projectID)
	for _, id := range sessionIDs {
		testdbseed.InsertSession(t, database, id, projectID)
	}
	return NewSQLQueue(database, maxRunning)
}
