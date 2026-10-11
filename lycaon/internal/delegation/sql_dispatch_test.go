package delegation

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSQLDispatchCommitsItsWorkerAndRetriesTheSameLeg(t *testing.T) {
	database := testdbfixture.Open(t, "dispatch.db")
	testdbseed.InsertSession(t, database, "coordinator", testdbseed.DefaultProjectID)
	store := NewSQLStore(database)
	d, err := store.Create(t.Context(), api.Delegation{ProjectID: testdbseed.DefaultProjectID, Task: "Review declaration", Strategy: api.HuntStrategyFileBased}, "coordinator", []api.Leg{{ID: "leg", Title: "Inspect declaration"}})
	testutil.FailErr(t, "create delegation", err)
	leg := d.Legs[0]
	leg.WorkerID, leg.Prompt = "first-job", "Inspect declaration"
	d.Phase = api.DelegationPhaseWorker
	task := api.WorkerTask{ID: leg.WorkerID, ProjectID: d.ProjectID, ParentSessionID: "coordinator", DelegationID: d.ID, LegID: leg.ID, Prompt: "Inspect declaration", Brief: "Inspect", ExecutionTarget: api.ExecutionTargetLocal}
	invalid := task
	invalid.Prompt = ""
	if err := store.DispatchLegWithJob(t.Context(), leg, *d, invalid); err == nil {
		t.Fatal("dispatch accepted invalid worker instructions")
	}
	pending, err := store.GetLeg(t.Context(), d.ID, leg.ID)
	testutil.FailErr(t, "read rolled back leg", err)
	if pending.Status != api.LegStatusPending || pending.WorkerID != "" {
		t.Fatalf("failed worker insertion changed leg: %+v", pending)
	}
	if _, err := db.New(database).GetWorkerJob(t.Context(), task.ID); !db.IsNoRows(err) {
		t.Fatalf("failed dispatch created worker: %v", err)
	}
	testutil.FailErr(t, "dispatch leg and job", store.DispatchLegWithJob(t.Context(), leg, *d, task))
	got, err := store.Get(t.Context(), d.ID)
	testutil.FailErr(t, "read dispatched delegation", err)
	if got.Phase != api.DelegationPhaseWorker || got.Legs[0].Status != api.LegStatusDispatched || got.Legs[0].WorkerID != task.ID {
		t.Fatalf("dispatch did not settle shared state: %+v legs=%+v", got, got.Legs)
	}
	job, err := db.New(database).GetWorkerJob(t.Context(), task.ID)
	testutil.FailErr(t, "read committed worker", err)
	if job.LegID.String != leg.ID || job.DelegationID.String != d.ID {
		t.Fatalf("worker attached to wrong leg: %+v", job)
	}
	duplicate := task
	duplicate.ID = "duplicate-job"
	leg.WorkerID = duplicate.ID
	if err := store.DispatchLegWithJob(t.Context(), leg, *d, duplicate); !errors.Is(err, ErrLegNotPending) {
		t.Fatalf("duplicate dispatch = %v", err)
	}
	if _, err := db.New(database).GetWorkerJob(t.Context(), duplicate.ID); !db.IsNoRows(err) {
		t.Fatalf("duplicate dispatch created worker: %v", err)
	}
	leg = got.Legs[0]
	leg.Status = api.LegStatusRetryPending
	testutil.FailErr(t, "record resumable leg", store.UpdateLeg(t.Context(), leg))
	replacement := task
	replacement.ID = "continuation-job"
	leg.WorkerID = replacement.ID
	testutil.FailErr(t, "redispatch continuation", store.RedispatchLegWithJob(t.Context(), leg, *d, replacement))
	continued, err := store.GetLeg(t.Context(), d.ID, leg.ID)
	testutil.FailErr(t, "read continuation", err)
	if continued.Status != api.LegStatusDispatched || continued.WorkerID != replacement.ID {
		t.Fatalf("continuation lost leg identity: %+v", continued)
	}
	job, err = db.New(database).GetWorkerJob(t.Context(), replacement.ID)
	testutil.FailErr(t, "read continuation job", err)
	if job.LegID.String != leg.ID {
		t.Fatalf("continuation created another leg: %+v", job)
	}
}

func TestSQLLegUpdatesPreserveDependenciesAndRefuseStaleWorkers(t *testing.T) {
	database := testdbfixture.Open(t, "leg-update.db")
	testdbseed.InsertSession(t, database, "coordinator", testdbseed.DefaultProjectID)
	store := NewSQLStore(database)
	d, err := store.Create(t.Context(), api.Delegation{ProjectID: testdbseed.DefaultProjectID, Task: "Review", Strategy: api.HuntStrategyFileBased}, "coordinator", []api.Leg{{ID: "first", Title: "First"}})
	testutil.FailErr(t, "create delegation", err)
	testutil.FailErr(t, "add dependent leg", store.AddLeg(t.Context(), d.ID, api.Leg{ID: "second", Title: "Second", DependsOn: []string{"first"}}))
	leg, err := store.GetLeg(t.Context(), d.ID, "second")
	testutil.FailErr(t, "read dependent leg", err)
	leg.Title, leg.Files = "Updated", []string{"source.go"}
	testutil.FailErr(t, "update dependent metadata", store.UpdateLeg(t.Context(), *leg))
	stale := *leg
	stale.WorkerID, stale.Title = "stale-job", "Ignored"
	testutil.FailErr(t, "submit stale worker metadata", store.UpdateLeg(t.Context(), stale))
	got, err := store.GetLeg(t.Context(), d.ID, leg.ID)
	testutil.FailErr(t, "read retained metadata", err)
	if got.Title != "Updated" || got.WorkerID != "" || len(got.DependsOn) != 1 || got.DependsOn[0] != "first" || len(got.Files) != 1 || got.Files[0] != "source.go" {
		t.Fatalf("stale update changed graph: %+v", got)
	}
	cycle := *got
	cycle.DependsOn = []string{"second"}
	if err := store.UpdateLeg(t.Context(), cycle); err == nil {
		t.Fatal("self-dependent leg update accepted")
	}
	if err := store.AddLeg(t.Context(), d.ID, api.Leg{ID: "invalid", DependsOn: []string{"missing"}}); err == nil {
		t.Fatal("unknown dependency accepted")
	}
	legs, err := store.ListLegs(t.Context(), d.ID)
	testutil.FailErr(t, "read preserved graph", err)
	if len(legs) != 2 {
		t.Fatalf("refused graph mutations changed legs: %+v", legs)
	}
}
