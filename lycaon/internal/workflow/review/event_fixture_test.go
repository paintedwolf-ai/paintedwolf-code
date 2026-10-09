package review_test

import (
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	workflow "github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"testing"
)

func newRunManagerForEvents(t *testing.T, name string) (*workflow.RunManager, *events.MemoryHub, *store.SQL, db.Handle) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, name)

	sessions := store.NewSQL(sqlDB)
	hub := events.NewMemoryHub()
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	return workflow.NewManager(workflowpersistence.New(sqlDB), sessions, reg, &events.Publisher{Hub: hub}), hub, sessions, sqlDB
}
