package store

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
)

func newSQLTestStore(t *testing.T, projectIDs ...string) *Repositories {
	t.Helper()
	database := testdbfixture.Open(t, "sessions.db")
	if len(projectIDs) == 0 {
		projectIDs = []string{testdbseed.DefaultProjectID}
	}
	for _, id := range projectIDs {
		testdbseed.InsertProjectRoot(t, database, id, t.TempDir())
	}
	return NewSQL(database)
}
