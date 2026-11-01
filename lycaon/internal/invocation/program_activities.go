package invocation

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lycaon/lycaon/pkg/api"
)

type activityInput struct {
	receipt                  api.CommandExecutionReceipt
	operation, family, scope string
	arguments                map[string]any
	span                     api.CommandActivitySpan
	pipeline                 bool
	pipelineStart            int
}

func activityNeedsAttention(receipt api.CommandExecutionReceipt) bool {
	return (receipt.StatusCode != nil && *receipt.StatusCode != 0) || receipt.State == "refused" || receipt.State == "failed" || receipt.State == "interrupted" || receipt.State == "waiting" || receipt.ApprovalRef != "" || receipt.HostStopCode != "" || receipt.StopCode != ""
}

func activityOutcome(receipt api.CommandExecutionReceipt) (string, string) {
	switch receipt.State {
	case "prepared":
		return "running", "Preparing"
	case "running":
		return "running", "Running"
	case "waiting":
		return "waiting", "Waiting"
	case "refused":
		return "refused", "Refused"
	case "failed":
		return "failed", "Failed"
	case "interrupted":
		return "interrupted", "Interrupted"
	case "completed":
		if receipt.StatusCode != nil {
			return "completed", fmt.Sprintf("Exited %d", *receipt.StatusCode)
		}
		return "completed", "Completed"
	default:
		return "unknown", "Outcome unknown"
	}
}

func readProgramActivities(ctx context.Context, tx *sql.Tx, program string, settled bool) ([]api.CommandActivity, error) {
	groups, err := readActivityGroups(ctx, tx, program)
	if err != nil {
		return nil, err
	}
	return composeActivityGroups(groups, settled), nil
}

// ActivitySummary publishes row identities with the result, before detail reads.
func (r *SQLRecorder) ActivitySummary(ctx context.Context, session, program string) (api.CommandProgramReceipt, error) {
	page, err := r.ReadProgramPage(ctx, session, program, "", 32)
	if err != nil {
		return api.CommandProgramReceipt{}, err
	}
	page.Program.Activities = page.Activities
	if len(page.Program.Activities) == 0 && page.Program.State != "running" && page.Program.State != "waiting" {
		state := page.Program.State
		if state == "rejected" {
			state = "refused"
		}
		normalized, outcome := activityOutcome(api.CommandExecutionReceipt{State: state, StatusCode: page.Program.StatusCode})
		page.Program.Activities = []api.CommandActivity{{ID: page.Program.ID + ":result", Group: "Running commands", Label: "Run command", Subject: page.Program.StopCode, State: normalized, Outcome: outcome, StatusCode: page.Program.StatusCode, ExecutionIDs: []string{}, Spans: []api.CommandActivitySpan{}}}
	}
	for _, receipt := range page.Executions {
		state, outcome := activityOutcome(receipt)
		page.Program.ActivityExecutions = append(page.Program.ActivityExecutions, api.CommandActivityExecution{ID: receipt.ID, State: state, Outcome: outcome, StatusCode: receipt.StatusCode})
	}

	return page.Program, nil
}

type activityObserverKey struct{}

// WithActivityObserver binds live presentation to this invocation's lifetime.
func WithActivityObserver(ctx context.Context, observe func(context.Context, string, string)) context.Context {
	return context.WithValue(ctx, activityObserverKey{}, observe)
}

func notifyActivity(ctx context.Context, program, execution string) {
	if observe, ok := ctx.Value(activityObserverKey{}).(func(context.Context, string, string)); ok {
		observe(ctx, program, execution)
	}
}
