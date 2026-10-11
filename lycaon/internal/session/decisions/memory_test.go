package decisions

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestMemoryPutGetClear(t *testing.T) {
	ctx := context.Background()
	s := NewMemory()
	if _, ok, err := s.Get(ctx, "c1"); err != nil || ok {
		t.Fatalf("empty store: ok=%v err=%v", ok, err)
	}
	err := s.Put(ctx, api.WorkerDecisionRequest{
		ChildSessionID: "c1", WorkerID: "job-1", Question: "Use A or B?", Options: []string{"A", "  ", "B"},
		BlockerClass: api.WorkerBlockerSandbox,
	})
	if err != nil {
		t.Fatalf("put decision: %v", err)
	}
	d, ok, err := s.Get(ctx, "c1")
	if err != nil {
		t.Fatalf("get decision: %v", err)
	}
	if !ok || d.Question != "Use A or B?" || d.WorkerID != "job-1" || len(d.Options) != 2 || d.BlockerClass != api.WorkerBlockerSandbox {
		t.Fatalf("decision = %+v ok = %v", d, ok)
	}
	if err := s.Put(ctx, api.WorkerDecisionRequest{ChildSessionID: "c2", Question: "ignored", Options: []string{"a", "b"}}); err == nil {
		t.Fatal("put without job_id succeeded")
	}
	if err := s.Clear(ctx, "c1"); err != nil {
		t.Fatalf("clear decision: %v", err)
	}
	if _, ok, err := s.Get(ctx, "c1"); err != nil || ok {
		t.Fatalf("cleared store: ok=%v err=%v", ok, err)
	}
}

func TestMemoryDetachesValuesAndScopesValuesByJob(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	decision := api.WorkerDecisionRequest{
		ChildSessionID: "child-1", WorkerID: "job-1", Question: "Choose", Options: []string{"A", "B"},
	}
	if err := store.Put(ctx, decision); err != nil {
		t.Fatalf("put decision: %v", err)
	}
	decision.Options[0] = "changed"
	loaded, ok, err := store.GetByJob(ctx, "job-1")
	if err != nil || !ok || loaded.Options[0] != "A" {
		t.Fatalf("loaded = %+v ok=%v err=%v", loaded, ok, err)
	}
	if err := store.Put(ctx, api.WorkerDecisionRequest{
		ChildSessionID: "child-2", WorkerID: "job-1", Question: "Other",
	}); err == nil {
		t.Fatal("one job was assigned to two children")
	}
}
