// Package findingstest opens a findings store on a fixture database.
package findingstest

import (
	"testing"

	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
)

// New returns the SQL findings store on a fresh fixture database in which
// each named session exists.
func New(t testing.TB, sessionIDs ...string) *findings.SQLStore {
	t.Helper()
	database := testdbfixture.Open(t, "findings.db")
	for _, id := range sessionIDs {
		testdbseed.InsertSession(t, database, id, testdbseed.DefaultProjectID)
	}
	return findings.NewSQLStore(database)
}
