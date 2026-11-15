package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workerplan"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

type receiptWorkflowChecker struct{}

func (receiptWorkflowChecker) AssertRunnable(context.Context, string) error { return nil }

func (receiptWorkflowChecker) AssertWorkerTask(context.Context, *api.WorkerTask) error { return nil }

func TestTaskToolReplaysReceiptAfterWorkflowMovedOn(t *testing.T) {
	var enqueued api.WorkerTask
	dir := t.TempDir()
	sqlDB := taskStore(t, dir)
	testdbseed.InsertWorkflowRun(t, sqlDB, "run", "parent-1", testdbseed.DefaultProjectID)
	bound := 0
	deps := worker.TaskToolDeps{
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
	reg := taskTool(t, sqlDB, func(q worker.WorkerQueue) worker.WorkerQueue {
		q.(*worker.SQLQueue).SetWorkflowDomains(&worker.WorkflowDomains{Runs: receiptWorkflowChecker{}, Tasks: receiptWorkflowChecker{}})
		return &captureQueue{WorkerQueue: q, out: &enqueued}
	}, deps)
	args := map[string]any{"agent_type": "repo-researcher", "brief": taskBrief("inspect"), "workflow_work_id": "leg-1", "scope": map[string]any{"mode": "read"}}
	tctx := toolContext("parent-1", dir)
	tctx.Identity.ToolCallID = "source-call"
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

type readyBlueprintApproval struct{ doc *api.Blueprint }

func (r readyBlueprintApproval) Get(context.Context, string, string) (*api.Blueprint, error) {
	return r.doc, nil
}
func (readyBlueprintApproval) IsApproved(context.Context, string, string) (bool, error) {
	return true, nil
}
func (readyBlueprintApproval) ImplementWorkflowReady(context.Context, string, string) (bool, error) {
	return true, nil
}

func TestTaskToolPlannedWorkerDispatchNameValidationAndReplay(t *testing.T) {
	var enqueued api.WorkerTask
	dir := t.TempDir()
	sqlDB := taskStore(t, dir)
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-plan", "parent-1", testdbseed.DefaultProjectID)

	bpContent := "# Plan\n<!-- lycaon:tasks [{\"id\":\"leg-1\",\"title\":\"Build API\",\"agent_type\":\"repo-researcher\",\"scope\":\"read\",\"files\":[\"internal/api.go\"]}] -->\n"
	plans := readyBlueprintApproval{doc: &api.Blueprint{Path: "plan.md", Content: bpContent}}

	records := workerrecords.New(sqlDB)
	planSvc := &workerplan.Service{
		Records:       records,
		Plans:         plans,
		Approvals:     plans,
		WorkflowReady: plans,
	}
	actor := workerrecords.Actor{SessionID: "parent-1", Role: workerrecords.RoleCoordinator, PrincipalID: "parent-1"}
	seeded, err := planSvc.PlanFromBlueprint(t.Context(), workerplan.SeedRequest{
		Actor:         actor,
		OperationID:   "seed-1",
		ProjectID:     testdbseed.DefaultProjectID,
		BlueprintPath: "plan.md",
		WorkspacePath: dir,
	})
	testutil.FailErr(t, "seed blueprint", err)
	plannedName := seeded.Legs[0].Worker

	reg := taskTool(t, sqlDB, func(q worker.WorkerQueue) worker.WorkerQueue {
		q.(*worker.SQLQueue).SetWorkflowDomains(&worker.WorkflowDomains{Runs: receiptWorkflowChecker{}, Tasks: receiptWorkflowChecker{}})
		return &captureQueue{WorkerQueue: q, out: &enqueued}
	}, worker.TaskToolDeps{
		Plan: planSvc,
		TaskReceipt: func(_ context.Context, parent, call string) (*api.WorkerTask, error) {
			if enqueued.SourceToolCallID == call && enqueued.ParentSessionID == parent {
				return &enqueued, nil
			}
			return nil, nil
		},
	})

	tctx := toolContext("parent-1", dir)
	tctx.Identity.ToolCallID = "bad-name"
	_, err = reg.Run(t.Context(), "task", map[string]any{
		"agent_type": "repo-researcher", "name": strings.Repeat("x", 65), "brief": taskBrief("inspect"), "scope": map[string]any{"mode": "read"},
	}, tctx)
	if rej := toolrejection.AsToolReject(err); rej == nil || rej.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("expected invalid name reject, got %v", err)
	}

	tctx.Identity.ToolCallID = "mismatch-name"
	_, err = reg.Run(t.Context(), "task", map[string]any{
		"agent_type": "repo-researcher", "name": "wrong-name", "brief": taskBrief("inspect"), "workflow_work_id": "leg-1", "scope": map[string]any{"mode": "read"},
	}, tctx)
	if rej := toolrejection.AsToolReject(err); rej == nil || rej.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("expected planned name mismatch reject, got %v", err)
	}

	tctx.Identity.ToolCallID = "planned-call-1"
	first, err := reg.Run(t.Context(), "task", map[string]any{
		"agent_type": "repo-researcher", "name": plannedName, "brief": taskBrief("inspect api"), "workflow_work_id": "leg-1", "scope": map[string]any{"mode": "read"},
	}, tctx)
	testutil.FailErr(t, "dispatch planned worker", err)
	replay, err := reg.Run(t.Context(), "task", map[string]any{
		"agent_type": "repo-researcher", "name": plannedName, "brief": taskBrief("inspect api"), "workflow_work_id": "leg-1", "scope": map[string]any{"mode": "read"},
	}, tctx)
	testutil.FailErr(t, "replay planned worker", err)
	if first != replay || len(enqueued.Files) != 1 || enqueued.Files[0] != "internal/api.go" {
		t.Fatalf("planned replay mismatch: first=%s replay=%s files=%v", first, replay, enqueued.Files)
	}
}
