package progresstest

import (
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func New(t testing.TB, sessionIDs ...string) *progress.SQLStore {
	t.Helper()
	database := testdbfixture.Open(t, "progress.db")
	for _, id := range sessionIDs {
		testdbseed.InsertWorkflowRun(t, database, "run-"+id, id, testdbseed.DefaultProjectID)
	}
	return progress.NewSQLStore(database, sessionstore.NewSQL(database))
}

func SeedRows(t testing.TB, store progress.Store, sessionID string, rows ...progress.RowSpec) progress.Snapshot {
	t.Helper()
	out, err := store.Apply(t.Context(), sessionID, progress.Mutation{Replace: rows}, progress.ApplyOptions{})
	testutil.FailErr(t, "apply progress rows", err)
	return out.Snapshot
}

func UpdateRows(t testing.TB, store progress.Store, sessionID string, updates ...progress.UpdateRowSpec) progress.Snapshot {
	t.Helper()
	out, err := store.Apply(t.Context(), sessionID, progress.Mutation{Update: updates}, progress.ApplyOptions{})
	testutil.FailErr(t, "update progress rows", err)
	return out.Snapshot
}

func State(s string) *string { return &s }

func Label(s string) *string { return &s }
