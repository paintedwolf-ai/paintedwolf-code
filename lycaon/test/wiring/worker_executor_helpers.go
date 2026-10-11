package wiring

import (
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/worker"
)

func workerTestInjectRenderer(t *testing.T) *prompts.InjectRenderer {
	t.Helper()
	return prompts.NewInjectRenderer(
		prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}),
	)
}

func wiringWorkerExecutor(h *Harness) *worker.LocalWorkerExecutor {
	exec := worker.NewLocalWorkerExecutor(h.Sessions.Manager.Workers, h.Delegations.Queue, h.Sessions.Manager.Workspace, h.Sessions.Manager.Submissions, h.Sessions.Manager.Runner.Transcript, h.Sessions.Manager.Runner.Execution, h.Sessions.Manager.Workers.Cancel, h.Sessions.Manager.Workers.Cancellations)
	exec.SetPromptInjects(prompts.NewInjectRenderer(
		prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}),
	))
	if h.Workflows.Manager != nil {
		exec.SetPhaseTouchPaths(h.Workflows.Manager.Ambient)
	}
	return exec
}
