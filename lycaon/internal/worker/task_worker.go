package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// TaskWorkerNameTakenCode refuses a task whose name another worker of the
// session already holds; that worker is directed with instruct_worker.
const TaskWorkerNameTakenCode = "TASK_WORKER_NAME_TAKEN"

func workerNameArg(args map[string]any) (string, error) {
	raw, ok := args["name"]
	if !ok || raw == nil {
		return "", nil
	}
	name, ok := raw.(string)
	name = strings.TrimSpace(name)
	if !ok || name == "" || len([]rune(name)) > 64 {
		return "", &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"tool": "task", "field": "name", "reason": "invalid_worker_name"}}
	}
	return name, nil
}

func taskWorkerOp(tctx tools.ToolContext, part string) workerrecords.Op {
	return workerrecords.Op{ID: "task:" + strings.TrimSpace(tctx.Identity.SessionID) + ":" + strings.TrimSpace(tctx.Identity.ToolCallID) + ":" + part}
}

func taskActor(tctx tools.ToolContext) workerrecords.Actor {
	id := strings.TrimSpace(tctx.Identity.SessionID)
	return workerrecords.Actor{SessionID: id, Role: workerrecords.RoleCoordinator, PrincipalID: id}
}

// createTaskWorker records the worker a task starts. Without a name the host
// derives one from the agent.
func createTaskWorker(ctx context.Context, deps TaskToolDeps, tctx tools.ToolContext, task api.WorkerTask, name, brief string) (workerrecords.WorkerReceipt, error) {
	if deps.WorkerRecords == nil {
		return workerrecords.WorkerReceipt{}, nil
	}
	if strings.TrimSpace(tctx.Identity.ToolCallID) == "" {
		return workerrecords.WorkerReceipt{}, fmt.Errorf("task requires a tool call identity")
	}
	mode := workerrecords.ScopeRead
	if task.EffectiveScope().IsWrite() {
		mode = workerrecords.ScopeWrite
	}
	receipt, err := deps.WorkerRecords.CreateWorker(ctx, taskActor(tctx), taskWorkerOp(tctx, "worker"), workerrecords.WorkerSpec{
		Name: name, AgentType: task.AgentType, ScopeMode: mode, Brief: brief,
		WorkflowRunID: task.WorkflowRunID, WorkflowPhase: task.WorkflowPhase, WorkflowWorkID: task.WorkflowWorkID,
	})
	if errors.Is(err, workerrecords.ErrNameTaken) {
		return receipt, &toolrejection.ToolReject{Code: TaskWorkerNameTakenCode, Data: map[string]any{"tool": "task", "worker": name}}
	}
	return receipt, err
}

// startWorkerRound admits the task's job as its worker's first round and
// returns the worker's name. A replayed task call finds the worker its first
// call created.
func startWorkerRound(ctx context.Context, deps TaskToolDeps, tctx tools.ToolContext, task api.WorkerTask) (string, error) {
	if deps.WorkerRecords == nil {
		return "", nil
	}
	actor := taskActor(tctx)
	kind, raw, ok, err := deps.WorkerRecords.Operation(ctx, actor, taskWorkerOp(tctx, "worker").ID)
	if err != nil {
		return "", err
	}
	if !ok || kind != "create_worker" {
		return "", fmt.Errorf("task %s started no worker", task.ID)
	}
	var w workerrecords.WorkerReceipt
	if err := json.Unmarshal(raw, &w); err != nil {
		return "", err
	}
	target := string(task.ExecutionTarget)
	if target == "" {
		target = string(api.ExecutionTargetLocal)
	}
	if _, err := deps.WorkerRecords.AdmitRound(ctx, actor, taskWorkerOp(tctx, "round"), w.WorkerID, workerrecords.RoundSpec{JobID: task.ID, ExecutionTarget: target}); err != nil {
		return "", err
	}
	return w.Name, nil
}
