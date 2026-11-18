package worker

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

func newTestQueue(t testing.TB, maxRunning int, projectID string, sessionIDs ...string) *SQLQueue {
	t.Helper()
	database := testdbfixture.Open(t, "workers.db")
	testdbseed.InsertProject(t, database, projectID)
	for _, id := range sessionIDs {
		testdbseed.InsertSession(t, database, id, projectID)
	}
	queue := NewSQLServices(database, maxRunning).Queue
	root := db.DataDir(database)
	queue.Branches.SetBaselineStore(workspacebaseline.New(database, sourceblob.New(filepath.Join(root, "source-content")), filepath.Join(root, "worker-baselines")))
	return queue
}
