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
}

func activityOutcome(state string, status *int) string {
	switch state {
	case "prepared":
		return "Preparing"
	case "completed", "failed":
		if status != nil {
			return fmt.Sprintf("Exited %d", *status)
		}
		return activityStateOutcome(state)
	case "running", "waiting", "refused", "interrupted":
		return activityStateOutcome(state)
	default:
		return "Outcome unknown"
	}
}

func failedActivityOutcome(activity api.CommandActivity) string {
	if activity.FailedCount == 1 && activity.Repetitions == 1 {
		for _, step := range activity.Steps {
			if step.ID == activity.ID && step.Failed == 1 {
				return step.Outcome
			}
		}
	}
	if activity.FailedCount < activity.ExecutionCount {
		return fmt.Sprintf("Finished · %d steps reported failure", activity.FailedCount)
	}
	return activityStateOutcome("failed")
}

func readProgramActivities(ctx context.Context, tx *sql.Tx, program api.CommandProgramReceipt, display func(string) string) ([]api.CommandActivity, error) {
	groups, err := readActivityGroups(ctx, tx, program.ID, display)
	if err != nil {
		return nil, err
	}
	state := activityProgramSettled
	if program.State == "running" || program.State == "waiting" {
		state = activityProgramRunning
	} else if program.State == "completed" && program.StatusCode != nil && *program.StatusCode == 0 {
		state = activityProgramSucceeded
	}
	activities := composeActivityGroups(groups, state)
	if len(activities) == 0 && state != activityProgramRunning {
		state := program.State
		if state == "rejected" {
			state = "refused"
		}
		outcome := activityOutcome(state, program.StatusCode)
		activities = []api.CommandActivity{{ID: program.ID, Group: "Running commands", Phase: "execute", Label: "Run command", Subject: program.StopCode, State: state, Outcome: outcome, StatusCode: program.StatusCode, ExecutionIDs: []string{}, Spans: []api.CommandActivitySpan{}, Steps: []api.CommandActivityStep{}}}
	}
	return activities, nil
}

// ActivitySummary reads complete counts without paging execution bodies.
func (r *SQLRecorder) ActivitySummary(ctx context.Context, session, program string) (api.CommandProgramReceipt, error) {
	display := r.pathDisplay(ctx, session)
	tx, err := r.beginProgramRead(ctx)
	if err != nil {
		return api.CommandProgramReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := readProgram(ctx, tx, session, program)
	if err != nil {
		return result, err
	}
	result.Activities, err = readProgramActivities(ctx, tx, result, display)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

type activityObserverKey struct{}

// ActivityObserver receives committed changes to a program's activities.
type ActivityObserver struct {
	Execution func(ctx context.Context, program, execution string)
	Settled   func(ctx context.Context, program string)
}

// WithActivityObserver binds live presentation to this invocation's lifetime.
// A program that outlives its call still reports through it until it settles.
func WithActivityObserver(ctx context.Context, observer ActivityObserver) context.Context {
	return context.WithValue(ctx, activityObserverKey{}, observer)
}

func notifyActivity(ctx context.Context, program, execution string) {
	if observer, ok := ctx.Value(activityObserverKey{}).(ActivityObserver); ok && observer.Execution != nil {
		observer.Execution(ctx, program, execution)
	}
}

func notifySettled(ctx context.Context, program string) {
	if observer, ok := ctx.Value(activityObserverKey{}).(ActivityObserver); ok && observer.Settled != nil {
		observer.Settled(ctx, program)
	}
}
