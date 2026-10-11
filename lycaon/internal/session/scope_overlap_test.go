package session_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTaskScopeCapExceeded(t *testing.T) {
	caps := workeradmission.ParallelTaskCaps{MaxRead: spawn.DefaultMaxReadTaskWorkers, MaxWrite: spawn.DefaultMaxWriteTaskWorkers}
	active := make([]api.WorkerTask, caps.MaxWrite)
	for i := range active {
		active[i] = api.WorkerTask{Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"pkg/a.go"}}}
	}
	if !workeradmission.TaskScopeCapExceeded(active, api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"pkg/b.go"}}, caps) {
		t.Fatal("expected write cap exceeded")
	}
}
