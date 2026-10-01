package delegation

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSQLStoreDelegationPersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "store.db")

	sqlDB := testdbfixture.OpenPath(t, dbPath)
	store := NewSQLStore(sqlDB)
	testdbseed.InsertSession(t, sqlDB, "sess-coord", testdbseed.DefaultProjectID)
	created, err := store.Create(context.Background(), api.Delegation{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: "/tmp/proj",
		Task:     "hunt",
		Strategy: api.HuntStrategyFileBased,
	}, "sess-coord", []api.Leg{{Title: "leg-1"}})
	testutil.FailErr(t, "create session in store", err)
	if created.ID == "" {
		t.Fatal("expected delegation id")
	}
	testutil.FailErr(t, "close database for reopen", sqlDB.Close())

	sqlDB2 := testdbfixture.OpenPath(t, dbPath)
	store2 := NewSQLStore(sqlDB2)
	got, err := store2.Get(context.Background(), created.ID)
	testutil.FailErr(t, "store2.Get failed", err)
	if got.Task != "hunt" || len(got.Legs) != 1 {
		t.Fatalf("delegation = %#v", got)
	}
	list, err := store2.ListByProject(context.Background(), testdbseed.DefaultProjectID, "")
	testutil.FailErr(t, "list reopened delegations", err)
	if len(list) != 1 {
		t.Fatalf("list = %#v", list)
	}
}

func TestSQLStoreScopesDelegationsAndLoadsTheirLegs(t *testing.T) {
	database := testdbfixture.Open(t, "delegation-scope.db")
	store := NewSQLStore(database)
	for _, sessionID := range []string{"selected", "other"} {
		testdbseed.InsertSession(t, database, sessionID, "p")
		_, err := store.Create(t.Context(), api.Delegation{ProjectID: "p", WorkspacePath: "/tmp/project",
			Task: sessionID, Strategy: api.HuntStrategyFileBased}, sessionID, []api.Leg{{Title: sessionID + "-first"}, {Title: sessionID + "-second"}})
		testutil.FailErr(t, "create delegation", err)
	}
	rows, err := store.ListByProject(t.Context(), "p", "selected")
	testutil.FailErr(t, "read selected session", err)
	if len(rows) != 1 || rows[0].Task != "selected" || len(rows[0].Legs) != 2 {
		t.Fatalf("selected delegations = %+v", rows)
	}
	titles := map[string]bool{}
	for _, leg := range rows[0].Legs {
		titles[leg.Title] = true
	}
	if !titles["selected-first"] || !titles["selected-second"] {
		t.Fatalf("selected legs = %+v", rows[0].Legs)
	}
	rows, err = store.ListByProject(t.Context(), "other-project", "selected")
	testutil.FailErr(t, "read foreign project", err)
	if len(rows) != 0 {
		t.Fatalf("cross-project delegations = %+v", rows)
	}
}

func TestSQLStoreCreateOnceReplaysItsOperationAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")
	sqlDB := testdbfixture.OpenPath(t, dbPath)
	testdbseed.InsertSession(t, sqlDB, "first", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, sqlDB, "second", testdbseed.DefaultProjectID)
	receipt := CreateReceipt{OperationID: "op-1", InputDigest: "digest-a"}
	draft := api.Delegation{ProjectID: testdbseed.DefaultProjectID, Task: "hunt", Strategy: api.HuntStrategyFileBased}
	created, err := NewSQLStore(sqlDB).CreateOnce(t.Context(), draft, "first", []api.Leg{{Title: "leg"}}, receipt)
	testutil.FailErr(t, "create under operation", err)
	testutil.FailErr(t, "close database for reopen", sqlDB.Close())

	reopened := NewSQLStore(testdbfixture.OpenPath(t, dbPath))
	replayed, err := reopened.CreateOnce(t.Context(), draft, "second", []api.Leg{{Title: "leg"}}, receipt)
	testutil.FailErr(t, "replay operation", err)
	if replayed.ID != created.ID || replayed.CoordinatorSessionID != "first" {
		t.Fatalf("replayed = %s in %s, want %s in first", replayed.ID, replayed.CoordinatorSessionID, created.ID)
	}
	found, ok, err := reopened.DelegationByOperation(t.Context(), receipt)
	testutil.FailErr(t, "look up operation", err)
	if !ok || found.ID != created.ID {
		t.Fatalf("operation lookup = %v %+v, want %s", ok, found, created.ID)
	}
	all, err := reopened.ListByProject(t.Context(), testdbseed.DefaultProjectID, "")
	testutil.FailErr(t, "list delegations", err)
	if len(all) != 1 {
		t.Fatalf("delegations = %d, want the one the operation created", len(all))
	}

	conflict := CreateReceipt{OperationID: "op-1", InputDigest: "digest-b"}
	if _, err := reopened.CreateOnce(t.Context(), draft, "second", []api.Leg{{Title: "leg"}}, conflict); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("create with a different request = %v, want ErrOperationConflict", err)
	}
	if _, _, err := reopened.DelegationByOperation(t.Context(), conflict); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("lookup with a different request = %v, want ErrOperationConflict", err)
	}
}
