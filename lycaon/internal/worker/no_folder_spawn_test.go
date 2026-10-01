package worker_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestApplyEnqueueDefaultsAllowsNoFolderWebResearcher(t *testing.T) {
	task := api.WorkerTask{
		AgentType: "web-researcher",
		Prompt:    "Research the question",
		Brief:     "Research the question",
		Scope:     &api.TaskScope{Mode: api.TaskScopeModeRead},
		ProjectID: "proj-1",
	}
	scope := project.ProjectScope{ProjectID: "proj-1", HasRoots: false}
	err := worker.ApplyEnqueueDefaults(&task, scope, worker.DefaultWorkersConfig())
	testutil.FailErr(t, "ApplyEnqueueDefaults", err)
}

func TestApplyEnqueueDefaultsWriteRequiresRoots(t *testing.T) {
	task := api.WorkerTask{
		AgentType: "implementer",
		Prompt:    "Update the project",
		Brief:     "Update the project",
		Scope:     &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"."}},
		ProjectID: "proj-1",
	}
	scope := project.ProjectScope{ProjectID: "proj-1", HasRoots: false}
	if err := worker.ApplyEnqueueDefaults(&task, scope, worker.DefaultWorkersConfig()); err == nil {
		t.Fatal("expected write without roots to fail")
	}
}

func TestApplyEnqueueDefaultsDefaultsAgentType(t *testing.T) {
	task := api.WorkerTask{
		Prompt:    "Inspect the project",
		Brief:     "Inspect the project",
		Scope:     &api.TaskScope{Mode: api.TaskScopeModeRead},
		ProjectID: "proj-1",
	}
	scope := project.ProjectScope{ProjectID: "proj-1", HasRoots: false}
	err := worker.ApplyEnqueueDefaults(&task, scope, worker.DefaultWorkersConfig())
	testutil.FailErr(t, "ApplyEnqueueDefaults", err)
	if task.AgentType != orchestration.ProfileImplementer {
		t.Fatalf("AgentType = %q, want %q", task.AgentType, orchestration.ProfileImplementer)
	}
}

func TestApplyEnqueueDefaultsRequiresInstructions(t *testing.T) {
	scope := project.ProjectScope{ProjectID: "proj-1"}
	for name, task := range map[string]api.WorkerTask{
		"prompt": {Brief: "Inspect the project"},
		"brief":  {Prompt: "Inspect the project"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := worker.ApplyEnqueueDefaults(&task, scope, worker.DefaultWorkersConfig()); err == nil {
				t.Fatalf("expected missing %s to fail", name)
			}
		})
	}
}
