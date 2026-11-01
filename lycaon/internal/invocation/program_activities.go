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

func readProgramActivities(ctx context.Context, tx *sql.Tx, program api.CommandProgramReceipt) ([]api.CommandActivity, error) {
	groups, err := readActivityGroups(ctx, tx, program.ID)
	if err != nil {
		return nil, err
	}
	settled := program.State != "running" && program.State != "waiting"
	activities := composeActivityGroups(groups, settled)
	if len(activities) == 0 && settled {
		state := program.State
		if state == "rejected" {
			state = "refused"
		}
		normalized, outcome := activityOutcome(api.CommandExecutionReceipt{State: state, StatusCode: program.StatusCode})
		activities = []api.CommandActivity{{ID: program.ID + ":result", Group: "Running commands", Label: "Run command", Subject: program.StopCode, State: normalized, Outcome: outcome, StatusCode: program.StatusCode, ExecutionIDs: []string{}, Spans: []api.CommandActivitySpan{}, Steps: []api.CommandActivityStep{}}}
	}
	return activities, nil
}

// ActivitySummary reads complete counts without paging execution bodies.
func (r *SQLRecorder) ActivitySummary(ctx context.Context, session, program string) (api.CommandProgramReceipt, error) {
	tx, err := r.beginProgramRead(ctx)
	if err != nil {
		return api.CommandProgramReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := readProgram(ctx, tx, session, program)
	if err != nil {
		return result, err
	}
	result.Activities, err = readProgramActivities(ctx, tx, result)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
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
