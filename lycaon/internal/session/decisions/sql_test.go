package decisions

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSQLSurvivesReconstruction(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertWorkerJob(t, database, "job-1", "child-1")

	store := NewSQL(database)
	testutil.FailErr(t, "put decision", store.Put(t.Context(), api.WorkerDecisionRequest{
		ChildSessionID: "child-1", WorkerID: "job-1", Question: "Choose one",
		Options: []string{"A", "B"}, BlockerClass: api.WorkerBlockerDecision,
	}))

	reopened := NewSQL(database)
	decision, ok, err := reopened.Get(t.Context(), "child-1")
	testutil.FailErr(t, "get decision", err)
	if !ok || decision.WorkerID != "job-1" || len(decision.Options) != 2 {
		t.Fatalf("decision = %+v ok=%v", decision, ok)
	}
}

func TestSQLPutGetClear(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertWorkerJob(t, database, "job-1", "c1")
	s := NewSQL(database)
	if _, ok, err := s.Get(ctx, "c1"); err != nil || ok {
		t.Fatalf("empty store: ok=%v err=%v", ok, err)
	}
	testutil.FailErr(t, "put decision", s.Put(ctx, api.WorkerDecisionRequest{
		ChildSessionID: "c1", WorkerID: "job-1", Question: "Use A or B?", Options: []string{"A", "  ", "B"},
		BlockerClass: api.WorkerBlockerSandbox,
	}))
	d, ok, err := s.Get(ctx, "c1")
	testutil.FailErr(t, "get decision", err)
	if !ok || d.Question != "Use A or B?" || d.WorkerID != "job-1" || len(d.Options) != 2 || d.BlockerClass != api.WorkerBlockerSandbox {
		t.Fatalf("decision = %+v ok = %v", d, ok)
	}
	if err := s.Put(ctx, api.WorkerDecisionRequest{ChildSessionID: "c1", Question: "ignored", Options: []string{"a", "b"}}); err == nil {
		t.Fatal("put without job_id succeeded")
	}
	testutil.FailErr(t, "clear decision", s.Clear(ctx, "c1"))
	if _, ok, err := s.Get(ctx, "c1"); err != nil || ok {
		t.Fatalf("cleared store: ok=%v err=%v", ok, err)
	}
}

func TestSQLAssignsEachJobToOneChild(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertWorkerJob(t, database, "job-1", "child-1")
	testdbseed.InsertSession(t, database, "child-2", testdbseed.DefaultProjectID)
	s := NewSQL(database)
	testutil.FailErr(t, "put decision", s.Put(ctx, api.WorkerDecisionRequest{
		ChildSessionID: "child-1", WorkerID: "job-1", Question: "Choose", Options: []string{"A", "B"},
	}))
	loaded, ok, err := s.GetByJob(ctx, "job-1")
	if err != nil || !ok || loaded.ChildSessionID != "child-1" || loaded.Options[0] != "A" {
		t.Fatalf("loaded = %+v ok=%v err=%v", loaded, ok, err)
	}
	if err := s.Put(ctx, api.WorkerDecisionRequest{
		ChildSessionID: "child-2", WorkerID: "job-1", Question: "Other",
	}); err == nil {
		t.Fatal("one job was assigned to two children")
	}
}
