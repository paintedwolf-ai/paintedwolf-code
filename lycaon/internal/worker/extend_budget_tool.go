package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	workerBudgetExtendNotRunningCode      = "WORKER_BUDGET_EXTEND_NOT_RUNNING"
	workerBudgetExtendNotIncreaseCode     = "WORKER_BUDGET_EXTEND_NOT_INCREASE"
	workerBudgetExtendInvalidCode         = "WORKER_BUDGET_EXTEND_INVALID"
	workerBudgetExtendSessionMismatchCode = "WORKER_BUDGET_EXTEND_SESSION_MISMATCH"
)

// ExtendBudgetToolDeps wires extend_worker_budget for coordinator sessions.
type ExtendBudgetToolDeps struct {
	Queue      WorkerQueue
	Ledger     BudgetLedger
	ToolBudget func(projectDir string) spawn.WorkerToolBudget
}

// RegisterExtendWorkerBudgetTool registers extend_worker_budget(job_id, max_tool_loops).
func RegisterExtendWorkerBudgetTool(reg *tools.DefaultRegistry, deps ExtendBudgetToolDeps) error {
	if reg == nil || deps.Queue == nil || deps.Ledger == nil {
		return fmt.Errorf("registry, queue, and budget ledger required")
	}
	return reg.Register("extend_worker_budget", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		jobID, _ := args["job_id"].(string)
		jobID = strings.TrimSpace(jobID)
		if jobID == "" {
			return "", fmt.Errorf("job_id is required")
		}
		newMax, err := session.ParseTaskMaxToolLoopsFromArgs(map[string]any{"max_tool_loops": args["max_tool_loops"]})
		if err != nil {
			return "", err
		}
		if newMax <= 0 {
			return "", fmt.Errorf("max_tool_loops is required")
		}
		task, ok := deps.Queue.Get(jobID)
		if !ok || task == nil {
			return "", &toolrejection.ToolReject{Code: workerBudgetExtendNotRunningCode, Data: map[string]any{"job_id": jobID}}
		}
		if strings.TrimSpace(task.ParentSessionID) != strings.TrimSpace(tctx.Identity.SessionID) {
			return "", &toolrejection.ToolReject{Code: workerBudgetExtendSessionMismatchCode, Data: map[string]any{"job_id": jobID}}
		}
		tctx.SetDisplaySubject(task.Brief)
		if !workerBudgetLive(task) || strings.TrimSpace(task.ChildSessionID) == "" {
			return "", &toolrejection.ToolReject{Code: workerBudgetExtendNotRunningCode, Data: map[string]any{"job_id": jobID, "status": string(task.Status)}}
		}
		budget := spawn.DefaultWorkerToolBudget()
		if deps.ToolBudget != nil {
			budget = deps.ToolBudget(tctx.ActiveRootPath())
		}
		if code := session.ValidateTaskMaxToolLoopsCode(newMax, budget); code != "" {
			return "", &toolrejection.ToolReject{
				Code: workerBudgetExtendInvalidCode,
				Data: map[string]any{
					"job_id":         jobID,
					"max_tool_loops": newMax,
					"host_max":       budget.Max,
					"min_required":   budget.Min,
				},
			}
		}
		current := budget.Effective(task.MaxToolLoops)
		if newMax <= current {
			return "", &toolrejection.ToolReject{
				Code: workerBudgetExtendNotIncreaseCode,
				Data: map[string]any{
					"job_id":               jobID,
					"max_tool_loops":       newMax,
					"current_max":          current,
					"host_max":             budget.Max,
					"worker_budget_at_max": current >= budget.Max,
				},
			}
		}
		if err := deps.Ledger.Grant(ctx, task.ChildSessionID, jobID, newMax); err != nil {
			if errors.Is(err, ErrWorkerBudgetNotLive) {
				return "", &toolrejection.ToolReject{Code: workerBudgetExtendNotRunningCode, Data: map[string]any{"job_id": jobID}}
			}
			return "", err
		}
		out := map[string]any{
			"job_id":           jobID,
			"child_session_id": strings.TrimSpace(task.ChildSessionID),
			"previous_max":     current,
			"new_max":          newMax,
			"tool_loops_used":  task.ToolLoopsUsed,
		}
		if task.BudgetRequest != nil {
			out["requested_max"] = task.BudgetRequest.RequestedMax
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return "", fmt.Errorf("encode extend_worker_budget result: %w", err)
		}
		return string(raw), nil
	})
}

// workerBudgetLive reports a job whose ceiling may still change.
func workerBudgetLive(task *api.WorkerTask) bool {
	return task != nil && (task.Status == api.WorkerStatusPending || task.Status == api.WorkerStatusRunning)
}
