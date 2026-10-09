package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
)

const (
	workerBudgetDeclineNoRequestCode       = "WORKER_BUDGET_DECLINE_NO_REQUEST"
	workerBudgetDeclineSessionMismatchCode = "WORKER_BUDGET_DECLINE_SESSION_MISMATCH"
)

// DeclineBudgetToolDeps wires decline_worker_budget for coordinator sessions.
type DeclineBudgetToolDeps struct {
	Queue  WorkerQueue
	Ledger BudgetLedger
}

// RegisterDeclineWorkerBudgetTool registers decline_worker_budget(job_id).
func RegisterDeclineWorkerBudgetTool(reg *tools.DefaultRegistry, deps DeclineBudgetToolDeps) error {
	if reg == nil || deps.Queue == nil || deps.Ledger == nil {
		return fmt.Errorf("registry, queue, and budget ledger required")
	}
	return reg.Register("decline_worker_budget", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		jobID, _ := args["job_id"].(string)
		jobID = strings.TrimSpace(jobID)
		if jobID == "" {
			return "", fmt.Errorf("job_id is required")
		}
		task, ok := deps.Queue.Get(jobID)
		if !ok || task == nil {
			return "", &toolrejection.ToolReject{Code: workerBudgetDeclineNoRequestCode, Data: map[string]any{"job_id": jobID}}
		}
		if strings.TrimSpace(task.ParentSessionID) != strings.TrimSpace(tctx.SessionID) {
			return "", &toolrejection.ToolReject{Code: workerBudgetDeclineSessionMismatchCode, Data: map[string]any{"job_id": jobID}}
		}
		tctx.SetDisplaySubject(task.Brief)
		declined, err := deps.Ledger.Decline(ctx, jobID)
		if err != nil {
			return "", err
		}
		if !declined {
			return "", &toolrejection.ToolReject{Code: workerBudgetDeclineNoRequestCode, Data: map[string]any{"job_id": jobID, "status": string(task.Status)}}
		}
		out := map[string]any{
			"job_id":          jobID,
			"max_tool_loops":  task.MaxToolLoops,
			"tool_loops_used": task.ToolLoopsUsed,
		}
		if task.BudgetRequest != nil {
			out["requested_max"] = task.BudgetRequest.RequestedMax
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return "", fmt.Errorf("encode decline_worker_budget result: %w", err)
		}
		return string(raw), nil
	})
}
