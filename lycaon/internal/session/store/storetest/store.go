// Package storetest constructs isolated SQL session stores.
package storetest

import (
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
)

// New opens a store with one attached root for each requested project.
func New(t *testing.T, projectIDs ...string) *store.Repositories {
	t.Helper()
	database := testdbfixture.Open(t, "sessions.db")
	for _, id := range projectIDs {
		testdbseed.InsertProjectRoot(t, database, id, t.TempDir())
	}
	return store.NewSQL(database)
}
