// Package workertest builds isolated queues over the production SQL store.
package workertest

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

// NewQueue seeds the declared project and sessions in a disposable database.
func NewQueue(t testing.TB, maxRunning int, projectID string, sessionIDs ...string) *worker.SQLQueue {
	t.Helper()
	database := testdbfixture.Open(t, "workers.db")
	testdbseed.InsertProject(t, database, projectID)
	for _, sessionID := range sessionIDs {
		testdbseed.InsertSession(t, database, sessionID, projectID)
	}
	return OnDatabase(t, database, maxRunning)
}

// OnDatabase shares durable worker and session state in one fixture installation.
func OnDatabase(t testing.TB, database db.Handle, maxRunning int) *worker.SQLQueue {
	t.Helper()
	queue := worker.NewSQLQueue(database, maxRunning)
	root := db.DataDir(database)
	queue.SetBaselineStore(workspacebaseline.New(database, sourceblob.New(filepath.Join(root, "source-content")), filepath.Join(root, "worker-baselines")))
	return queue
}
