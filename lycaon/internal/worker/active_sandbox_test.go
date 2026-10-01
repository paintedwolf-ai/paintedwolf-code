package worker_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestActiveSandboxJobIDs(t *testing.T) {
	tasks := []api.WorkerTask{
		{ID: "live-run", Status: api.WorkerStatusRunning, WorkspaceRoot: "/tmp/w1", Scope: writeScope()},
		{ID: "await-promote", Status: api.WorkerStatusComplete, WorkspaceRoot: "/tmp/w2", Scope: writeScope(), MergeStatus: api.WorkerMergeStatusPending},
		{ID: "merged", Status: api.WorkerStatusComplete, WorkspaceRoot: "/tmp/w3", Scope: writeScope(), MergeStatus: api.WorkerMergeStatusMerged},
		{ID: "failed", Status: api.WorkerStatusFailed, WorkspaceRoot: "/tmp/w4", Scope: writeScope()},
		{ID: "read-only", Status: api.WorkerStatusRunning, WorkspaceRoot: "/tmp/w5", Scope: &api.TaskScope{Mode: api.TaskScopeModeRead}},
	}
	got := worker.ActiveSandboxJobIDs(tasks)
	for _, id := range []string{"live-run", "await-promote"} {
		if _, ok := got[id]; !ok {
			t.Fatalf("ActiveSandboxJobIDs missing %q: %v", id, got)
		}
	}
	for _, id := range []string{"merged", "failed", "read-only"} {
		if _, ok := got[id]; ok {
			t.Fatalf("ActiveSandboxJobIDs should not retain %q: %v", id, got)
		}
	}
}

func writeScope() *api.TaskScope {
	return &api.TaskScope{Mode: api.TaskScopeModeWrite}
}
