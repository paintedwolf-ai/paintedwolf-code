package worker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerops"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workerstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// Coordinator tools that act on long-lived workers.
const (
	InstructWorkerTool = "instruct_worker"
	ShelveOverlayTool  = "shelve_overlay"
	WorkerHistoryTool  = "worker_history"
)

// WorkerToolDeps wires the worker tools.
type WorkerToolDeps struct {
	Ops     *workerops.Service
	Queue   WorkerQueue
	Workers WorkersConfig
}

// RegisterWorkerTools registers instruct_worker, shelve_overlay and
// worker_history.
func RegisterWorkerTools(reg *tools.DefaultRegistry, deps WorkerToolDeps) error {
	if reg == nil || deps.Ops == nil || deps.Queue == nil {
		return fmt.Errorf("registry, worker operations and queue required")
	}
	for name, h := range map[string]tools.ToolHandler{
		InstructWorkerTool: instructWorkerHandler(deps),
		ShelveOverlayTool:  shelveOverlayHandler(deps),
		WorkerHistoryTool:  workerHistoryHandler(deps),
	} {
		if err := reg.Register(name, h); err != nil {
			return err
		}
	}
	return nil
}

func coordinatorActor(tctx tools.ToolContext) workerrecords.Actor {
	id := strings.TrimSpace(tctx.Identity.SessionID)
	return workerrecords.Actor{SessionID: id, Role: workerrecords.RoleCoordinator, PrincipalID: id}
}

// toolOp is a tool call's operation identity: a replayed call returns its
// first receipt.
func toolOp(tool string, tctx tools.ToolContext) (workerrecords.Op, error) {
	call := strings.TrimSpace(tctx.Identity.ToolCallID)
	if call == "" {
		return workerrecords.Op{}, fmt.Errorf("%s requires a tool call identity", tool)
	}
	return workerrecords.Op{ID: tool + ":" + strings.TrimSpace(tctx.Identity.SessionID) + ":" + call}, nil
}

func stringArgOf(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

func instructWorkerHandler(deps WorkerToolDeps) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		op, err := toolOp(InstructWorkerTool, tctx)
		if err != nil {
			return "", err
		}
		in := workerops.Instruction{
			Worker: stringArgOf(args, "worker"), Instructions: stringArgOf(args, "instructions"),
			Brief: stringArgOf(args, "brief"), AgentType: stringArgOf(args, "agent_type"),
		}
		if in.Worker == "" || in.Instructions == "" {
			return "", fmt.Errorf("worker and instructions are required")
		}
		receipt, err := deps.Ops.Instruct(ctx, coordinatorActor(tctx), op, in, roundEnqueuer(deps, tctx, args))
		if err != nil {
			return "", workerToolReject(InstructWorkerTool, in.Worker, in.AgentType, err)
		}
		if tctx.Effects.Out != nil && receipt.JobID != "" {
			tctx.Effects.Out.OwnerRef = receipt.JobID
		}
		raw, err := json.Marshal(receipt)
		return string(raw), err
	}
}

// roundEnqueuer enqueues a new round's job on the worker's own child session.
// The job's prompt is the instruction, with the new brief when the goal
// changed; the conversation carries everything else.
func roundEnqueuer(deps WorkerToolDeps, tctx tools.ToolContext, args map[string]any) workerops.Enqueuer {
	return func(ctx context.Context, job workerops.RoundJob) (string, string, error) {
		w := job.Worker
		agent, mode := w.AgentType, w.ScopeMode
		if job.AgentType != "" {
			agent, mode = job.AgentType, job.ScopeMode
		}
		prompt := job.Instructions
		// A worker started without a standing brief works toward its instruction.
		goal := w.Brief
		if goal == "" {
			goal = job.Instructions
		}
		if job.Brief != "" {
			goal = job.Brief
			prompt = formatTaskCharter(api.WorkerTaskCharter{Goal: job.Brief}) + "\n\n" + job.Instructions
		}
		digest, err := instructArgsDigest(args, tctx)
		if err != nil {
			return "", "", err
		}
		scope := project.ScopeFromToolContext(tctx.Identity.ProjectID, tctx.Source.ActiveRootID, tctx.Source.Roots, tctx.ActiveRootPath())
		task := api.WorkerTask{
			ID:               uuid.NewString(),
			ParentSessionID:  w.SessionID,
			ChildSessionID:   w.ChildSessionID,
			AgentType:        agent,
			Prompt:           prompt,
			Brief:            goal,
			Scope:            &api.TaskScope{Mode: api.TaskScopeMode(mode)},
			ProjectID:        scope.ProjectID,
			WorkspaceRootID:  scope.WorkspaceRootID,
			WorkspacePath:    scope.WorkspacePath,
			SpawnReason:      api.SpawnReasonHumanRequest,
			Status:           api.WorkerStatusPending,
			ExecutionTarget:  DefaultExecutionTarget(deps.Workers),
			SourceToolCallID: strings.TrimSpace(tctx.Identity.ToolCallID),
			SourceArgsDigest: digest,
		}
		if err := ApplyEnqueueDefaults(&task, scope, deps.Workers); err != nil {
			return "", "", err
		}
		id, err := deps.Queue.Enqueue(ctx, task)
		if err != nil {
			return "", "", err
		}
		return id, string(task.ExecutionTarget), nil
	}
}

func instructArgsDigest(args map[string]any, tctx tools.ToolContext) (string, error) {
	raw, err := json.Marshal(struct {
		Tool      string         `json:"tool"`
		SessionID string         `json:"session_id"`
		Args      map[string]any `json:"args"`
	}{InstructWorkerTool, strings.TrimSpace(tctx.Identity.SessionID), args})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:]), nil
}

func shelveOverlayHandler(deps WorkerToolDeps) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		op, err := toolOp(ShelveOverlayTool, tctx)
		if err != nil {
			return "", err
		}
		handle, reason := stringArgOf(args, "overlay_id"), stringArgOf(args, "reason")
		if handle == "" || reason == "" {
			return "", fmt.Errorf("overlay_id and reason are required")
		}
		d, err := deps.Ops.Shelve(ctx, coordinatorActor(tctx), op, handle, reason)
		if err != nil {
			return "", workerToolReject(ShelveOverlayTool, "", "", err, handle)
		}
		raw, err := json.Marshal(map[string]any{"overlay_id": d.ResultID, "status": d.ChangeState, "reason": d.ShelveReason, "accounted": d.AccountedAt != ""})
		return string(raw), err
	}
}

func workerHistoryHandler(deps WorkerToolDeps) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		name := stringArgOf(args, "worker")
		if name == "" {
			return "", fmt.Errorf("worker is required")
		}
		before, _ := args["before"].(float64)
		limit, _ := args["limit"].(float64)
		page, err := deps.Ops.History(ctx, coordinatorActor(tctx), name, int64(before), int(limit))
		if err != nil {
			return "", workerToolReject(WorkerHistoryTool, name, "", err)
		}
		rounds := make([]map[string]any, 0, len(page.Rounds))
		for _, r := range page.Rounds {
			row := map[string]any{"round": r.Ordinal, "settled": r.SettledAt != ""}
			if r.ResultID != "" {
				row["overlay_id"] = r.ResultID
				row["changed"] = r.CheckpointID != ""
			}
			rounds = append(rounds, row)
		}
		messages := make([]map[string]any, 0, len(page.Messages))
		for _, m := range page.Messages {
			row := map[string]any{"seq": m.Seq, "from": m.SenderRole, "round": m.RoundOrdinal, "text": m.Body}
			if m.NeedsReply {
				row["needs_reply"] = true
			}
			if m.ReplyToSeq > 0 {
				row["reply_to"] = m.ReplyToSeq
			}
			messages = append(messages, row)
		}
		out := map[string]any{
			"worker": page.Worker.Name, "agent_type": page.Worker.AgentType, "scope_mode": page.Worker.ScopeMode,
			"state": page.State.State, "brief": page.Worker.Brief, "rounds": rounds, "messages": messages,
		}
		if page.State.Reason != workerstate.ReasonNone {
			out["reason"] = page.State.Reason
		}
		if page.Before > 0 {
			out["before"] = page.Before
		}
		raw, err := json.Marshal(out)
		return string(raw), err
	}
}

// instructRefusalCodes name each state an instruction cannot reach.
var instructRefusalCodes = map[workerstate.State]string{
	workerstate.Asking:     "INSTRUCT_WORKER_ASKING",
	workerstate.Waiting:    "INSTRUCT_WORKER_WAITING",
	workerstate.Planned:    "INSTRUCT_WORKER_PLANNED",
	workerstate.Recovering: "INSTRUCT_WORKER_RECOVERING",
	workerstate.Closed:     "INSTRUCT_WORKER_CLOSED",
}

var agentChangeCodes = map[workerstate.AgentChange]string{
	workerstate.AgentChangeSealed:      "INSTRUCT_WORKER_AGENT_SEALED",
	workerstate.AgentChangeBusy:        "INSTRUCT_WORKER_AGENT_BETWEEN_ROUNDS",
	workerstate.AgentChangeOpenChanges: "INSTRUCT_WORKER_AGENT_CHANGES_OPEN",
}

// workerToolReject maps operation errors to typed rejections. Unmapped errors
// pass through unchanged.
func workerToolReject(tool, worker, agent string, err error, handle ...string) error {
	var refusal *workerops.Refusal
	switch {
	case errors.Is(err, workerrecords.ErrWorkerNotFound):
		return &toolrejection.ToolReject{Code: "WORKER_NAME_UNKNOWN", Data: map[string]any{"tool": tool, "worker": worker}}
	case errors.Is(err, workerrecords.ErrResultNotFound):
		return &toolrejection.ToolReject{Code: "RESULT_HANDLE_UNKNOWN", Data: map[string]any{"tool": tool, "overlay_id": firstOf(handle)}}
	case errors.Is(err, workerops.ErrUnknownAgent):
		return &toolrejection.ToolReject{Code: "WORKER_TYPE_UNAVAILABLE", Data: map[string]any{"agent_type": agent, "reason": "unknown_agent_type"}}
	case errors.Is(err, workerops.ErrNoChanges):
		return &toolrejection.ToolReject{Code: "SHELVE_OVERLAY_NO_CHANGES", Data: map[string]any{"overlay_id": firstOf(handle)}}
	case errors.As(err, &refusal):
		data := map[string]any{"tool": tool, "worker": refusal.Worker, "state": string(refusal.View.State), "admitted": admittedOps(refusal.View),
			"overlay_id": refusal.ChangedResult, "workflow_work_id": refusal.WorkflowWorkID}
		if refusal.View.Reason != workerstate.ReasonNone {
			data["reason"] = string(refusal.View.Reason)
		}
		switch refusal.Op {
		case workerstate.OpCancel:
			return &toolrejection.ToolReject{Code: "WORKER_CANCEL_NOT_RUNNING", Data: data}
		case workerstate.OpAnswer:
			return &toolrejection.ToolReject{Code: DecisionNotFoundCode, Data: data}
		case workerstate.OpPreview, workerstate.OpPromote, workerstate.OpReject, workerstate.OpShelve:
			return &toolrejection.ToolReject{Code: "RESULT_CHANGES_NOT_ADMITTED", Data: data}
		}
		if code, ok := agentChangeCodes[refusal.AgentChange]; ok {
			return &toolrejection.ToolReject{Code: code, Data: data}
		}
		if code, ok := instructRefusalCodes[refusal.View.State]; ok {
			return &toolrejection.ToolReject{Code: code, Data: data}
		}
	}
	return err
}

func admittedOps(v workerstate.View) []string {
	out := make([]string, 0, len(v.Ops))
	for _, op := range v.Ops {
		out = append(out, string(op))
	}
	return out
}

func firstOf(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
