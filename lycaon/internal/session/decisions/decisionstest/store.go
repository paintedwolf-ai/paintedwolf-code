// Package decisionstest opens a worker decision store on a fixture database.
package decisionstest

import (
	"testing"

	"github.com/lycaon/lycaon/internal/session/decisions"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
)

// Job names a worker job and its child session.
type Job struct{ ID, Child string }

// New returns the SQL decision store on a fresh fixture database holding each
// named job and child session.
func New(t testing.TB, jobs ...Job) *decisions.SQL {
	t.Helper()
	database := testdbfixture.Open(t, "decisions.db")
	for _, job := range jobs {
		testdbseed.InsertWorkerJob(t, database, job.ID, job.Child)
	}
	return decisions.NewSQL(database)
}
