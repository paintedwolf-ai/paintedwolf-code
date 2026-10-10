package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

type receiptWorkflowChecker struct{}

func (receiptWorkflowChecker) AssertRunnable(context.Context, string) error { return nil }

func (receiptWorkflowChecker) AssertWorkerTask(context.Context, *api.WorkerTask) error { return nil }

func TestTaskToolReplaysReceiptAfterWorkflowMovedOn(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	var enqueued api.WorkerTask
	inner := worker.NewInMemoryQueue(2)
	inner.SetWorkflowRunChecker(receiptWorkflowChecker{})
	queue := &captureQueue{WorkerQueue: inner, out: &enqueued}
	bound := 0
	deps := worker.TaskToolDeps{
		Sessions: &fakeTaskSessions{}, Queue: queue, Agents: orchestration.NewMemoryAgentRegistryForTest(), Workers: worker.DefaultWorkersConfig(),
		BindWorkflowTask: func(_ context.Context, _ tools.ToolContext, id string, task *api.WorkerTask) error {
			bound++
			if bound > 1 {
				return errors.New("workflow has moved on")
			}
			task.WorkflowRunID, task.WorkflowPhase, task.WorkflowWorkID = "run", "execute", id
			return nil
		},
		ComposePrompt: func(_ context.Context, _ tools.ToolContext, _ string, _ api.WorkerTaskCharter, task *api.WorkerTask) (string, error) {
			if bound != 1 || task.WorkflowRunID != "run" || task.WorkflowWorkID != "leg-1" {
				t.Fatal("prompt composed before workflow binding")
			}
			return "Bound assignment", nil
		},
		TaskReceipt: func(_ context.Context, parent, call string) (*api.WorkerTask, error) {
			if enqueued.SourceToolCallID == call && enqueued.ParentSessionID == parent {
				return &enqueued, nil
			}
			return nil, nil
		},
	}
	testutil.FailErr(t, "register", worker.RegisterTaskTool(reg, deps))
	args := map[string]any{"agent_type": "repo-researcher", "brief": taskBrief("inspect"), "workflow_work_id": "leg-1", "scope": map[string]any{"mode": "read"}}
	tctx := toolContext("parent-1", t.TempDir())
	tctx.ToolCallID = "source-call"
	first, err := reg.Run(t.Context(), "task", args, tctx)
	testutil.FailErr(t, "first dispatch", err)
	replay, err := reg.Run(t.Context(), "task", args, tctx)
	testutil.FailErr(t, "replay after phase change", err)
	var a, b map[string]any
	testutil.FailErr(t, "decode first", json.Unmarshal([]byte(first), &a))
	testutil.FailErr(t, "decode replay", json.Unmarshal([]byte(replay), &b))
	if a["job_id"] != b["job_id"] || bound != 1 {
		t.Fatalf("replay created work: %s; %s; bound=%d", first, replay, bound)
	}
	args["workflow_work_id"] = "leg-2"
	if _, err = reg.Run(t.Context(), "task", args, tctx); err == nil {
		t.Fatal("receipt accepted changed arguments")
	}
}
