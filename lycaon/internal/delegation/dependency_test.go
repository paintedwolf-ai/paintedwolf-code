package delegation

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestValidateLegDependenciesRejectsUnknownAndCycles(t *testing.T) {
	if err := ValidateLegDependencies([]api.Leg{{ID: "a", DependsOn: []string{"missing"}}}); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown dependency error = %v", err)
	}
	if err := ValidateLegDependencies([]api.Leg{{ID: "a", DependsOn: []string{"b"}}, {ID: "b", DependsOn: []string{"a"}}}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error = %v", err)
	}
}

func TestValidateLegDependenciesRejectsSurroundingWhitespace(t *testing.T) {
	if err := ValidateLegDependencies([]api.Leg{{ID: " upstream"}}); err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("leg id whitespace error = %v", err)
	}
	if err := ValidateLegDependencies([]api.Leg{{ID: "downstream", DependsOn: []string{" upstream"}}, {ID: "upstream"}}); err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("dependency whitespace error = %v", err)
	}
}

func TestDependencyDispatchGateWaitsForIntegratedUpstream(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	created, err := store.Create(ctx, api.Delegation{ProjectID: "project"}, "session", []api.Leg{
		{ID: "upstream", Status: api.LegStatusPending, WorkerID: "worker"},
		{ID: "dependent", Status: api.LegStatusPending, DependsOn: []string{"upstream"}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	gate := DependencyDispatchGate{Store: store, Workers: dependencyWorkerSource{job: &api.WorkerTask{ID: "worker", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{Status: "complete"}}}}
	allowed, reason, err := gate.Check(ctx, created.ID, "dependent")
	if err != nil || allowed || !strings.Contains(reason, "upstream") {
		t.Fatalf("before upstream complete = (%v, %q, %v)", allowed, reason, err)
	}
	upstream, err := store.GetLeg(ctx, created.ID, "upstream")
	if err != nil {
		t.Fatalf("get upstream: %v", err)
	}
	upstream.Status = api.LegStatusComplete
	upstream.WorkerID = "worker"
	if err := store.UpdateLeg(ctx, *upstream); err != nil {
		t.Fatalf("complete upstream: %v", err)
	}
	allowed, reason, err = gate.Check(ctx, created.ID, "dependent")
	if err != nil || !allowed || reason != "" {
		t.Fatalf("after upstream complete = (%v, %q, %v)", allowed, reason, err)
	}
}

func TestDependencyDispatchGateDoesNotReleaseAfterPartialUpstream(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	created, err := store.Create(ctx, api.Delegation{ProjectID: "project"}, "session", []api.Leg{
		{ID: "upstream", Status: api.LegStatusRunning, WorkerID: "worker-upstream"},
		{ID: "dependent", Status: api.LegStatusPending, DependsOn: []string{"upstream"}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	manager := &Manager{Store: store}
	if err := manager.RecordOutcome(ctx, created.ID, "upstream", "worker-upstream", api.WorkerResult{Status: "partial"}); err != nil {
		t.Fatalf("record partial outcome: %v", err)
	}
	allowed, reason, err := (DependencyDispatchGate{Store: store}).Check(ctx, created.ID, "dependent")
	if err != nil || allowed || !strings.Contains(reason, "upstream") {
		t.Fatalf("partial upstream = (%v, %q, %v)", allowed, reason, err)
	}
}

type dependencyWorkerSource struct{ job *api.WorkerTask }

func (s dependencyWorkerSource) Get(id string) (*api.WorkerTask, bool) {
	return s.job, s.job != nil && s.job.ID == id
}

func TestCompletedLegStillWaitsForWritePromotion(t *testing.T) {
	store := NewMemoryStore()
	created, err := store.Create(t.Context(), api.Delegation{ProjectID: "project"}, "session", []api.Leg{{ID: "upstream", WorkerID: "worker", Status: api.LegStatusComplete}, {ID: "consumer", Status: api.LegStatusPending, DependsOn: []string{"upstream"}}})
	if err != nil {
		t.Fatalf("create delegation: %v", err)
	}
	job := &api.WorkerTask{ID: "worker", Status: api.WorkerStatusComplete, Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite}, Result: &api.WorkerResult{Status: "open"}, MergeStatus: api.WorkerMergeStatusPending}
	gate := DependencyDispatchGate{Store: store, Workers: dependencyWorkerSource{job: job}}
	ready, _, err := gate.Check(t.Context(), created.ID, "consumer")
	if err != nil || ready {
		t.Fatalf("admitted unpromoted output: %v, %v", ready, err)
	}
	job.MergeStatus = api.WorkerMergeStatusMerged
	ready, _, err = gate.Check(t.Context(), created.ID, "consumer")
	if err != nil || !ready {
		t.Fatalf("refused promoted output: %v, %v", ready, err)
	}
}
