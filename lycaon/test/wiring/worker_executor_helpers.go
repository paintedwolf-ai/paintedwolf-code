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
	exec := worker.NewLocalWorkerExecutor(h.SessionMgr, h.WorkerQueue)
	exec.SetPromptInjects(prompts.NewInjectRenderer(
		prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}),
	))
	if h.WorkflowMgr != nil {
		exec.SetPhaseTouchPaths(h.WorkflowMgr.Ambient)
	}
	return exec
}
