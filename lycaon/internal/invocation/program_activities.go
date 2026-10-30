package invocation

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

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
	for i := range inputs {
		if inputs[i].receipt.State == "prepared" && !inputs[i].receipt.OwnerStarted {
			continue
		}
		rule := activityPresentation(inputs[i])
		if rule.role == "primary" || activityNeedsAttention(inputs[i].receipt) {
			owners[inputs[i].receipt.ParentExecutionID] = append(owners[inputs[i].receipt.ParentExecutionID], i)
		}
	}
	indices := make(map[int]int)
	for i := range inputs {
		input := inputs[i]
		if input.receipt.State == "prepared" && !input.receipt.OwnerStarted {
			continue
		}
		rule := activityPresentation(input)
		if rule.role == "container" || (rule.role == "context" && !activityNeedsAttention(input.receipt)) {
			continue
		}
		owner := i
		if rule.role != "primary" && !activityNeedsAttention(input.receipt) {
			candidates := owners[input.receipt.ParentExecutionID]
			if rule.role == "context" && len(owners) > 0 {
				continue
			}
			if len(candidates) > 0 {
				owner = supportingActivityOwner(inputs, i, candidates)
			} else if rule.role == "support" && input.pipeline {
				continue
			}
		}
		index, exists := indices[owner]
		if !exists {
			primary := inputs[owner]
			presentation := activityPresentation(primary)
			state, outcome := activityOutcome(primary.receipt)
			activities = append(activities, api.CommandActivity{
				ID: primary.receipt.ID, Group: presentation.group, Label: presentation.label,
				Subject: activitySubject(primary), Directory: stringArgument(primary.arguments, "cwd"),
				State: state, Outcome: outcome, StatusCode: primary.receipt.StatusCode,
				ExecutionIDs: []string{}, Spans: []api.CommandActivitySpan{}, MergeKey: primary.mergeKey,
			})
			index = len(activities) - 1
			indices[owner] = index
		}
		activity := &activities[index]
		activity.ExecutionIDs = append(activity.ExecutionIDs, input.receipt.ID)
		if input.span.SourceRef != "" {
			activity.Spans = append(activity.Spans, input.span)
		}
		if owner != i && activityNeedsAttention(input.receipt) {
			activity.State, activity.Outcome = activityOutcome(input.receipt)
			activity.MergeKey = ""
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
	return receipt.State == "refused" || receipt.State == "failed" || receipt.State == "interrupted" || receipt.State == "waiting" || receipt.ApprovalRef != "" || receipt.HostStopCode != "" || receipt.StopCode != ""
}

func activityOutcome(receipt api.CommandExecutionReceipt) (string, string) {
	switch receipt.State {
	case "prepared", "running":
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
	result := composeActivities(inputs, settled)
	// First-touch order is independent of result arrival.
	slices.SortStableFunc(result, func(a, b api.CommandActivity) int {
		return activityInputOrder(inputs, a.ID) - activityInputOrder(inputs, b.ID)
	})
	return result, nil
}

func activityInputOrder(inputs []activityInput, id string) int {
	for i := range inputs {
		if inputs[i].receipt.ID == id {
			return i
		}
	}
	return len(inputs)
}

// ActivitySummary publishes row identities with the result, before detail reads.
func (r *SQLRecorder) ActivitySummary(ctx context.Context, session, program string) (api.CommandProgramReceipt, error) {
	page, err := r.ReadProgramPage(ctx, session, program, "", 32)
	if err != nil {
		return api.CommandProgramReceipt{}, err
	}
	page.Program.Activities = page.Activities
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
func WithActivityObserver(ctx context.Context, observe func(context.Context, string)) context.Context {
	return context.WithValue(ctx, activityObserverKey{}, observe)
}

func notifyActivity(ctx context.Context, program string) {
	if observe, ok := ctx.Value(activityObserverKey{}).(func(context.Context, string)); ok {
		observe(ctx, program)
	}
}
