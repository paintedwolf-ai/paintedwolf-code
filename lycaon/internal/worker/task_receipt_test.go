package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workerops"
	"github.com/lycaon/lycaon/internal/workerplan"
	"github.com/lycaon/lycaon/internal/workerplan/plantest"
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
	planDeps := plantest.Deps(records)
	planDeps.Plans, planDeps.Approvals, planDeps.WorkflowReady = plans, plans, plans
	planSvc := plantest.New(t, planDeps)
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

	tctx.Identity.ToolCallID = "files-call-1"
	_, err = reg.Run(t.Context(), "task", map[string]any{
		"agent_type": "repo-researcher", "brief": taskBrief("inspect extra"),
		"files": []any{"internal/extra.go"}, "scope": map[string]any{"mode": "read"},
	}, tctx)
	testutil.FailErr(t, "dispatch explicit files", err)
	if len(enqueued.Files) != 1 || enqueued.Files[0] != "internal/extra.go" {
		t.Fatalf("explicit files mismatch: %v", enqueued.Files)
	}

	q := worker.NewSQLServices(sqlDB, 1).Queue
	q.SetWorkflowDomains(&worker.WorkflowDomains{Runs: receiptWorkflowChecker{}, Tasks: receiptWorkflowChecker{}})
	ops := &workerops.Service{
		Reader:     workerops.Reader{Records: records, Jobs: q},
		Agents:     prompts.AgentMutationCapable,
		WakeParked: func(context.Context, string) error { return nil },
		Release:    q.ReleaseAdmitted,
	}
	now := timelayout.Format(time.Now().UTC())
	_, err = sqlDB.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, agent_type, created_at, activity_at, updated_at)
		VALUES ('planned-child', ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'parent-1', 'repo-researcher', ?, ?, ?)`, testdbseed.DefaultProjectID, now, now, now)
	testutil.FailErr(t, "insert planned child", err)
	w, err := records.Resolve(t.Context(), actor, plannedName)
	testutil.FailErr(t, "resolve planned worker", err)
	rounds, err := records.Rounds(t.Context(), actor, w.ID)
	testutil.FailErr(t, "list planned rounds", err)
	_, err = sqlDB.ExecContext(t.Context(), `UPDATE workers SET child_session_id = 'planned-child' WHERE id = ?`, w.ID)
	testutil.FailErr(t, "bind planned child", err)
	_, err = sqlDB.ExecContext(t.Context(), `UPDATE worker_jobs SET child_session_id = 'planned-child', status = 'complete', completed_at = ? WHERE id = ?`, now, rounds[0].JobID)
	testutil.FailErr(t, "complete first planned job", err)
	_, err = db.New(sqlDB).SettleWorkerRoundByJob(t.Context(), db.SettleWorkerRoundByJobParams{
		SettledAt: db.NullString(now), JobID: rounds[0].JobID,
	})
	testutil.FailErr(t, "settle first planned round", err)

	boundInstruct := false
	wReg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register worker tools", worker.RegisterWorkerTools(wReg, worker.WorkerToolDeps{
		Ops:   ops,
		Queue: q,
		Plan:  planSvc,
		BindWorkflowTask: func(_ context.Context, _ tools.ToolContext, workID string, task *api.WorkerTask) error {
			boundInstruct = workID == "leg-1"
			task.WorkflowRunID = "run-plan"
			return nil
		},
	}))
	tctx.Identity.ToolCallID = "instruct-planned-1"
	_, err = wReg.Run(t.Context(), worker.InstructWorkerTool, map[string]any{
		"worker": plannedName, "instructions": "re-check internal/api.go",
	}, tctx)
	testutil.FailErr(t, "instruct planned worker", err)
	if !boundInstruct {
		t.Fatal("expected BindWorkflowTask on planned worker instruction")
	}
}
