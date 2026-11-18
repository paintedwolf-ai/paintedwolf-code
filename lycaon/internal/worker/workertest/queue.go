// Package workertest builds isolated queues over the production SQL store.
package workertest

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/worker"
)

// NewQueue seeds the declared project and sessions in a disposable database.
func NewQueue(t testing.TB, maxRunning int, projectID string, sessionIDs ...string) *worker.SQLQueue {
	t.Helper()
	database := testdbfixture.Open(t, "workers.db")
	testdbseed.InsertProject(t, database, projectID)
	for _, sessionID := range sessionIDs {
		testdbseed.InsertSession(t, database, sessionID, projectID)
	}
	return worker.NewSQLQueue(database, maxRunning)
}
