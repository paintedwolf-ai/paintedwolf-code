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
	mergeKey                 string
	pipeline                 bool
	pipelineStart            int
}

// composeActivities preserves execution identity while folding supporting steps.
func composeActivities(inputs []activityInput, settled bool) []api.CommandActivity {
	activities := make([]api.CommandActivity, 0, len(inputs))
	owners := make(map[string][]int)
	indices := make(map[int]int)
	for i := range inputs {
		input := inputs[i]
		rule := activityPresentation(input)
		if rule.role == "container" || (rule.role == "context" && !activityNeedsAttention(input.receipt)) {
			continue
		}
		owner := i
		if rule.role == "support" {
			candidates := owners[input.receipt.ParentExecutionID]
			if len(candidates) > 0 {
				owner = supportingActivityOwner(inputs, i, candidates)
			}
		}
		index, exists := indices[owner]
		if !exists {
			primary := inputs[owner]
			presentation := activityPresentation(primary)
			state, outcome := activityOutcome(primary.receipt)
			activities = append(activities, api.CommandActivity{
				ID: primary.receipt.ID, Group: presentation.group, Label: presentation.label,
				Subject: activitySubject(primary), JobHandle: primary.receipt.JobHandle, Directory: stringArgument(primary.arguments, "cwd"),
				State: state, Outcome: outcome, StatusCode: primary.receipt.StatusCode,
				ExecutionIDs: []string{}, Spans: []api.CommandActivitySpan{}, MergeKey: primary.mergeKey,
			})
			index = len(activities) - 1
			indices[owner] = index
		}
		if owner == i {
			owners[input.receipt.ParentExecutionID] = append(owners[input.receipt.ParentExecutionID], i)
		}
		activity := &activities[index]
		activity.ExecutionIDs = append(activity.ExecutionIDs, input.receipt.ID)
		if input.span.SourceRef != "" {
			activity.Spans = append(activity.Spans, input.span)
		}
		if owner != i && activityNeedsAttention(input.receipt) {
			activity.State, activity.Outcome = activityOutcome(input.receipt)
			activity.StatusCode = input.receipt.StatusCode
		} else if owner != i && (input.receipt.State == "running" || input.receipt.State == "prepared") && activity.State == "completed" {
			activity.State, activity.Outcome = "running", "Processing output"
		}
	}
	// A control-only program still has a visible result.
	if len(activities) == 0 && len(inputs) > 0 && settled {
		input := inputs[0]
		state, outcome := activityOutcome(input.receipt)
		activities = append(activities, api.CommandActivity{ID: input.receipt.ID, Group: "Running commands", Label: "Run command", Subject: input.receipt.Command, State: state, Outcome: outcome, ExecutionIDs: []string{input.receipt.ID}, Spans: []api.CommandActivitySpan{input.span}})
	}
	return activities
}

func supportingActivityOwner(inputs []activityInput, index int, candidates []int) int {
	owner := candidates[0]
	for _, candidate := range candidates {
		if inputs[candidate].span.SourceRef == inputs[index].span.SourceRef && inputs[candidate].span.StartByte <= inputs[index].span.StartByte && inputs[candidate].span.StartByte >= inputs[owner].span.StartByte {
			owner = candidate
		}
	}
	return owner
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

func readProgramActivities(ctx context.Context, tx *sql.Tx, executions []api.CommandExecutionReceipt, settled bool) ([]api.CommandActivity, error) {
	inputs := make([]activityInput, 0, len(executions))
	for _, receipt := range executions {
		input, err := readActivityInput(ctx, tx, receipt)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, input)
	}
	return composeActivities(inputs, settled), nil
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
	page.Program.ActivitiesCursor = page.NextCursor
	page.Program.ActivitiesOmitted = page.ExecutionsOmitted
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
