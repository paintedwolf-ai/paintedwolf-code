package worker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// TaskToolDeps wires the native task spawn tool.
type TaskToolDeps struct {
	Queue            WorkerQueue
	Agents           orchestration.AgentRegistry
	Workers          WorkersConfig
	ToolBudget       func(projectDir string) spawn.WorkerToolBudget
	ComposePrompt    func(ctx context.Context, tctx tools.ToolContext, agentType string, brief api.WorkerTaskCharter, workerJobID string, scope *api.TaskScope, maxToolLoops int) (string, error)
	WebSearchEnabled func() bool
	// PendingDecision returns a child's open decision.
	PendingDecision  func(context.Context, string) (jobID string, ok bool, err error)
	BindWorkflowTask func(context.Context, tools.ToolContext, string, *api.WorkerTask) error
	// WorkflowWork resolves dispatch constraints for an active workflow work id.
	WorkflowWork func(context.Context, string, string) (spawn.WorkflowWork, bool, error)
	TaskReceipt  func(context.Context, string, string) (*api.WorkerTask, error)
}

var taskToolLog = observability.LazyComponent("task_tool")

// overlayDiscarded reports whether a write overlay was removed.
func overlayDiscarded(status api.WorkerMergeStatus) bool {
	switch status {
	case api.WorkerMergeStatusRejected, api.WorkerMergeStatusAborted:
		return true
	default:
		return false
	}
}

// RegisterTaskTool registers task(agent_type, brief, files?) for coordinator spawns.
func RegisterTaskTool(reg *tools.DefaultRegistry, deps TaskToolDeps) error {
	if reg == nil || deps.Queue == nil || deps.Agents == nil {
		return fmt.Errorf("registry, sessions, queue, and agents required")
	}
	return reg.Register("task", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		sourceDigest, err := taskToolArgsDigest(args, tctx)
		if err != nil {
			return "", err
		}
		if deps.TaskReceipt != nil && strings.TrimSpace(tctx.ToolCallID) != "" {
			prior, err := deps.TaskReceipt(ctx, tctx.SessionID, strings.TrimSpace(tctx.ToolCallID))
			if err != nil {
				return "", err
			}
			if prior != nil {
				if err := compareTaskReceipt(prior, api.WorkerTask{SourceToolCallID: tctx.ToolCallID, SourceArgsDigest: sourceDigest}); err != nil {
					return "", err
				}
				scope := project.ScopeFromToolContext(tctx.ProjectID, tctx.ActiveRootID, tctx.Roots, tctx.ActiveRootPath())
				return taskEnqueueOutput(tctx, scope, *prior)
			}
		}
		maxToolLoops, err := workeradmission.ParseTaskMaxToolLoopsFromArgs(args)
		if err != nil {
			return "", err
		}
		budget := spawn.DefaultWorkerToolBudget()
		if deps.ToolBudget != nil {
			budget = deps.ToolBudget(tctx.ActiveRootPath())
		}
		if code := workeradmission.ValidateTaskMaxToolLoopsCode(maxToolLoops, budget); code != "" {
			return "", &tools.ToolReject{Code: code, Data: map[string]any{"max_tool_loops": maxToolLoops, "min_required": budget.Min, "host_max": budget.Max}}
		}
		identity, err := resolveTaskIdentity(ctx, deps, tctx, args, maxToolLoops, budget)
		if err != nil {
			return "", err
		}
		agentType := identity.AgentType
		if _, err := deps.Agents.Get(agentType); err != nil {
			return "", &tools.ToolReject{
				Code: "WORKER_TYPE_UNAVAILABLE",
				Data: map[string]any{"agent_type": agentType, "reason": "unknown_agent_type"},
			}
		}
		brief, err := plannedTaskCharter(identity.Charter, args)
		if err != nil {
			return "", err
		}
		after, err := parseAfterWorkers(args)
		if err != nil {
			return "", err
		}
		prompt := formatTaskCharter(brief)
		var files []string
		if raw, ok := args["files"].([]any); ok {
			for _, item := range raw {
				if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
					files = append(files, strings.TrimSpace(s))
				}
			}
		}
		childSessionID, _ := args["child_session_id"].(string)
		childSessionID = strings.TrimSpace(childSessionID)
		if childSessionID != "" && deps.PendingDecision != nil {
			pendingJobID, ok, err := deps.PendingDecision(ctx, childSessionID)
			if err != nil {
				return "", fmt.Errorf("load pending decision: %w", err)
			}
			if ok {
				return "", &tools.ToolReject{
					Code: "TASK_DECISION_PENDING",
					Data: map[string]any{
						"child_session_id": childSessionID,
						"job_id":           pendingJobID,
					},
				}
			}
		}
		normalizedScope := identity.Scope
		effectiveMaxToolLoops := identity.MaxToolLoops
		scopePtr := &normalizedScope
		jobID := uuid.NewString()
		if deps.ComposePrompt != nil {
			composed, err := deps.ComposePrompt(ctx, tctx, agentType, brief, jobID, scopePtr, effectiveMaxToolLoops)
			if err != nil {
				return "", err
			}
			prompt = composed
		}
		if prior := identity.Prior; prior != nil && overlayDiscarded(prior.MergeStatus) {
			return "", &tools.ToolReject{
				Code: "WORKER_RESUME_OVERLAY_DISCARDED",
				Data: map[string]any{
					"child_session_id": childSessionID,
					"prior_job_id":     prior.ID,
					"merge_status":     string(prior.MergeStatus),
				},
			}
		}

		projScope := project.ScopeFromToolContext(tctx.ProjectID, tctx.ActiveRootID, tctx.Roots, tctx.ActiveRootPath())
		if !projScope.HasRoots && !prompts.AgentRunsWithoutWorkspace(agentType) {
			return "", &tools.ToolReject{
				Code: "WORKER_WORKSPACE_REQUIRED",
				Data: map[string]any{"agent_type": agentType, "reason": "project_roots_required"},
			}
		}
		if agentdef.DeclaresAny(agentType, agentdef.CapabilityExternal) && deps.WebSearchEnabled != nil && !deps.WebSearchEnabled() {
			return "", &tools.ToolReject{
				Code: "WEB_SEARCH_DISABLED",
				Data: map[string]any{"agent_type": agentType},
			}
		}
		task := api.WorkerTask{
			ID:              jobID,
			AfterWorkers:    after,
			ParentSessionID: tctx.SessionID,
			ChildSessionID:  childSessionID,
			AgentType:       agentType,
			Prompt:          prompt,
			Brief:           brief.Goal,
			Scope:           scopePtr,
			Files:           files,
			ProjectID:       projScope.ProjectID,
			WorkspaceRootID: projScope.WorkspaceRootID,
			WorkspacePath:   projScope.WorkspacePath,
			SpawnReason:     api.SpawnReasonHumanRequest,
			Status:          api.WorkerStatusPending,
			ExecutionTarget: DefaultExecutionTarget(deps.Workers),
			MaxToolLoops:    effectiveMaxToolLoops,
		}
		if err := ApplyEnqueueDefaults(&task, projScope, deps.Workers); err != nil {
			return "", err
		}
		task.SourceToolCallID = strings.TrimSpace(tctx.ToolCallID)
		if deps.BindWorkflowTask != nil {
			if err := deps.BindWorkflowTask(ctx, tctx, identity.WorkflowWorkID, &task); err != nil {
				return "", err
			}
		}
		task.SourceArgsDigest = sourceDigest
		enqueuedID, err := deps.Queue.Enqueue(ctx, task)
		if err != nil {
			return "", err
		}
		task.ID = enqueuedID
		return taskEnqueueOutput(tctx, projScope, task)
	})
}

func taskEnqueueOutput(tctx tools.ToolContext, scope project.ProjectScope, task api.WorkerTask) (string, error) {
	jobID, childSessionID, agentType := task.ID, task.ChildSessionID, task.AgentType
	if tctx.Out != nil {
		tctx.Out.OwnerRef = jobID
		tctx.Out.Dispatch = &api.WorkerDispatch{
			WorkerID: jobID, ChildSessionID: childSessionID, AgentType: agentType,
		}
	}
	taskToolLog.Info("task enqueued",
		"parent_session_id", tctx.SessionID,
		"job_id", jobID,
		"agent_type", agentType,
		"project_id", scope.ProjectID,
		"workspace_path", scope.WorkspacePath,
	)

	out := map[string]any{
		"agent_type":         agentType,
		"job_id":             jobID,
		"status":             "enqueued",
		"scope_mode":         task.EffectiveScope().Mode,
		"after_workers":      task.AfterWorkers,
		"sibling_files_live": false,
	}
	if task.EffectiveScope().IsWrite() {
		out["workspace_kind"] = "private_snapshot"
	} else {
		out["workspace_kind"] = "primary"
	}
	if childSessionID != "" {
		out["child_session_id"] = childSessionID
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("encode task result: %w", err)
	}
	return string(raw), nil
}

func taskToolArgsDigest(args map[string]any, tctx tools.ToolContext) (string, error) {
	canonical := struct {
		SessionID    string         `json:"session_id"`
		ProjectID    string         `json:"project_id"`
		ActiveRootID string         `json:"active_root_id"`
		Args         map[string]any `json:"args"`
	}{
		SessionID: strings.TrimSpace(tctx.SessionID), ProjectID: strings.TrimSpace(tctx.ProjectID),
		ActiveRootID: strings.TrimSpace(tctx.ActiveRootID), Args: args,
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("encode task arguments: %w", err)
	}
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("%x", digest[:]), nil
}
