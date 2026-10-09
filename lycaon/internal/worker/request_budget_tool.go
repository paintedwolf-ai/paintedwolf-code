package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// RequestBudgetTool is the worker's ask for a higher tool-round ceiling.
const RequestBudgetTool = "request_budget"

const (
	requestBudgetAddressedSessionCode = "REQUEST_BUDGET_ADDRESSED_SESSION"
	workerBudgetRequestOpenCode       = "WORKER_BUDGET_REQUEST_OPEN"
	workerBudgetRequestAtHostMaxCode  = "WORKER_BUDGET_REQUEST_AT_HOST_MAX"
)

// RequestBudgetToolDeps wires request_budget for worker sessions.
type RequestBudgetToolDeps struct {
	Queue      WorkerQueue
	Ledger     BudgetLedger
	ToolBudget func(projectDir string) spawn.WorkerToolBudget
	// Notify wakes the coordinator that owns the job's budget.
	Notify func(ctx context.Context, task api.WorkerTask)
	Now    func() time.Time
}

// RegisterRequestBudgetTool registers request_budget(rounds, remaining_work).
func RegisterRequestBudgetTool(reg *tools.DefaultRegistry, deps RequestBudgetToolDeps) error {
	if reg == nil || deps.Queue == nil || deps.Ledger == nil || deps.Notify == nil {
		return fmt.Errorf("registry, queue, budget ledger, and notify required")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return reg.Register(RequestBudgetTool, func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		jobID := strings.TrimSpace(tctx.Identity.WorkerJobID)
		if tools.OutOfSessionScope(RequestBudgetTool, tctx) || jobID == "" {
			return "", &toolrejection.ToolReject{Code: requestBudgetAddressedSessionCode, Data: map[string]any{"tool": RequestBudgetTool}}
		}
		rounds, err := session.ParseTaskMaxToolLoopsFromArgs(map[string]any{"max_tool_loops": args["rounds"]})
		if err != nil || rounds <= 0 {
			return "", fmt.Errorf("rounds must be a positive integer")
		}
		remaining := stringItems(args["remaining_work"])
		if len(remaining) == 0 {
			return "", fmt.Errorf("remaining_work must name the work the extra rounds cover")
		}
		task, ok := deps.Queue.Get(jobID)
		if !ok || task == nil || !workerBudgetLive(task) {
			return "", fmt.Errorf("worker job %s is not running", jobID)
		}
		budget := spawn.DefaultWorkerToolBudget()
		if deps.ToolBudget != nil {
			budget = deps.ToolBudget(tctx.ActiveRootPath())
		}
		current := budget.Effective(task.MaxToolLoops)
		if current >= budget.Max {
			return "", &toolrejection.ToolReject{Code: workerBudgetRequestAtHostMaxCode, Data: map[string]any{
				"max_tool_loops": current, "host_max": budget.Max,
			}}
		}
		if open := task.BudgetRequest; open != nil {
			return "", &toolrejection.ToolReject{Code: workerBudgetRequestOpenCode, Data: map[string]any{
				"requested_max": open.RequestedMax, "max_tool_loops": current,
			}}
		}
		req := api.WorkerBudgetRequest{
			Rounds:        rounds,
			RequestedMax:  min(current+rounds, budget.Max),
			RemainingWork: remaining,
			ToolLoopsUsed: task.ToolLoopsUsed,
			RequestedAt:   deps.Now().UTC(),
		}
		recorded, err := deps.Ledger.Request(ctx, jobID, req)
		if err != nil {
			return "", err
		}
		if !recorded {
			return "", &toolrejection.ToolReject{Code: workerBudgetRequestOpenCode, Data: map[string]any{"max_tool_loops": current}}
		}
		task.BudgetRequest = &req
		deps.Notify(ctx, *task)
		raw, err := json.Marshal(map[string]any{
			"status":          "requested",
			"max_tool_loops":  current,
			"requested_max":   req.RequestedMax,
			"tool_loops_used": req.ToolLoopsUsed,
		})
		if err != nil {
			return "", fmt.Errorf("encode request_budget result: %w", err)
		}
		return string(raw), nil
	})
}

func stringItems(raw any) []string {
	items, _ := raw.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}
