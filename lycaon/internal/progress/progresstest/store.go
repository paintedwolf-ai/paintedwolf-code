package progresstest

import (
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
)

func New(t testing.TB, sessionIDs ...string) *progress.SQLStore {
	t.Helper()
	database := testdbfixture.Open(t, "progress.db")
	for _, id := range sessionIDs {
		testdbseed.InsertSession(t, database, id, testdbseed.DefaultProjectID)
	}
	return progress.NewSQLStore(database)
}
