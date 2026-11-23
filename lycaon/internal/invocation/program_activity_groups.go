package invocation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/pkg/api"
)

type activityKey struct {
	node, operation, scope, launch string
}

type activityGroup struct {
	input       activityInput
	key, parent activityKey
	ordinal     int64
	counts      activityCounts
}

func readActivityGroups(ctx context.Context, tx *sql.Tx, program string, display func(string) string) ([]activityGroup, error) {
	groups, err := readActivityCounts(ctx, tx, program)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		g := &groups[i]
		receipts, _, err := readExecutions(ctx, tx, program, g.ordinal-1, 0, 1, executionSelection{})
		if err != nil {
			return nil, err
		}
		if len(receipts) != 1 {
			return nil, fmt.Errorf("activity representative is unavailable")
		}
		g.input, err = readActivityInput(ctx, tx, receipts[0], display)
		if err != nil {
			return nil, err
		}
		g.key = activityKey{node: receipts[0].NodeID, operation: g.input.operation, scope: g.input.scope}
		if g.input.operation == "program &" {
			g.key = activityKey{launch: receipts[0].ID}
		}
		var node, operation, scope string
		err = tx.QueryRowContext(ctx, `SELECT node_id,operation_id,scope_json FROM invocation_program_executions WHERE program_id=? AND id=?`, program, receipts[0].ParentExecutionID).Scan(&node, &operation, &scope)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			g.parent = activityKey{node: node, operation: operation, scope: scope}
			if operation == "program &" {
				g.parent = activityKey{launch: receipts[0].ParentExecutionID}
			}
		}
	}
	return groups, nil
}

func groupActivity(g activityGroup) api.CommandActivity {
	rule := activityPresentation(g.input)
	state := activityStepState(g.counts)
	outcome := activityStateOutcome(state)
	if g.input.operation == "program &" && state == "completed" {
		outcome = "Started"
	}
	return api.CommandActivity{ID: g.input.receipt.ID, Group: rule.group, Phase: rule.phase, Label: rule.label, Subject: activitySubject(g.input), Directory: stringArgument(g.input.arguments, "cwd"), JobHandle: g.input.receipt.JobHandle, State: state, Outcome: outcome, ExecutionIDs: []string{}, Spans: []api.CommandActivitySpan{}, Repetitions: g.counts.total, Steps: []api.CommandActivityStep{}}
}

// Precedence over folded steps decides the row state; membership never does.
var activityStateRank = map[string]int{"unknown": 0, "completed": 0, "failed": 1, "interrupted": 2, "refused": 3, "running": 4, "waiting": 5}

func activityStepState(c activityCounts) string {
	switch {
	case c.waiting > 0:
		return "waiting"
	case c.running > 0:
		return "running"
	case c.refused > 0:
		return "refused"
	case c.interrupted > 0:
		return "interrupted"
	case c.failed > 0:
		return "failed"
	default:
		return "completed"
	}
}

func activityStateOutcome(state string) string {
	switch state {
	case "waiting":
		return "Waiting"
	case "running":
		return "Running"
	case "refused":
		return "Refused"
	case "interrupted":
		return "Interrupted"
	case "failed":
		return "Failed"
	default:
		return "Finished"
	}
}

func appendActivityStep(a *api.CommandActivity, g activityGroup) {
	c := g.counts
	state := activityStepState(c)
	outcome := activityStateOutcome(state)
	switch {
	case state == "running" && g.input.receipt.State == "prepared":
		outcome = activityOutcome("prepared", nil)
	case state == "completed", state == "failed" && c.total == 1:
		outcome = activityOutcome(state, g.input.receipt.StatusCode)
	}
	a.ExecutionCount += c.total
	a.FailedCount += c.failed + c.refused + c.interrupted
	a.WaitingCount += c.waiting
	a.UncertainEffects += c.uncertain
	a.CompletedEffects += c.effects
	a.ExecutionIDs = append(a.ExecutionIDs, g.input.receipt.ID)
	a.Spans = append(a.Spans, g.input.span)
	a.Steps = append(a.Steps, api.CommandActivityStep{ID: g.input.receipt.ID, Command: activityStepLabel(g), Count: c.total, Running: c.running, Waiting: c.waiting, Failed: c.failed, Refused: c.refused, Interrupted: c.interrupted, State: state, Outcome: outcome})
	if activityStateRank[state] > activityStateRank[a.State] {
		a.State, a.Outcome = state, activityStateOutcome(state)
	}
}

type activityProgramState int

const (
	activityProgramRunning activityProgramState = iota
	activityProgramSettled
	activityProgramSucceeded
)

// Supporting steps follow recorded ancestry; sequential primaries fold by phase.
func composeActivityGroups(groups []activityGroup, state activityProgramState) []api.CommandActivity {
	byKey := make(map[activityKey]int, len(groups))
	for i, g := range groups {
		byKey[g.key] = i
	}
	primary := make(map[int]bool)
	for i, g := range groups {
		if activityPresentation(g.input).role == "primary" {
			primary[i] = true
		}
	}
	// A background job's work belongs to the activity that launched it.
	launched := make(map[int]int)
	subjects := make(map[int]string)
	for i, g := range groups {
		launch, ok := launchOf(g, groups, byKey)
		if !ok && state == activityProgramSucceeded {
			groups[i].counts.recoverUtilityExits()
		}
		if !ok || activityPresentation(g.input).role == "container" {
			continue
		}
		launched[i] = launch
		// The job is named by the first process it runs.
		if _, named := subjects[launch]; !named && primary[i] && activityPresentation(g.input).phase == "execute" {
			if subject := activitySubject(g.input); subject != "" {
				subjects[launch] = subject
			}
		}
		delete(primary, i)
	}
	assignments := make(map[int]int, len(groups))
	for i, g := range groups {
		if launch, ok := launched[i]; ok {
			assignments[i] = launch
			continue
		}
		if primary[i] {
			assignments[i] = i
			continue
		}
		if activityPresentation(g.input).role == "container" {
			continue
		}
		// A redirected producer supports the write its own statement made: the
		// redirect whose source span lies within the producer's statement.
		if j := statementRedirect(groups, primary, g); j >= 0 {
			assignments[i] = j
			continue
		}
		parent := g.parent
		seen := map[activityKey]bool{g.key: true}
		for parent != (activityKey{}) && !seen[parent] {
			seen[parent] = true
			p, ok := byKey[parent]
			if !ok {
				break
			}
			if primary[p] {
				assignments[i] = p
				break
			}
			// The first primary in this subtree remains the anchor as work arrives.
			candidate := -1
			for j := range groups {
				if primary[j] && groupDescendsFrom(groups[j], parent, groups, byKey) {
					candidate = j
					break
				}
			}
			if candidate >= 0 {
				assignments[i] = candidate
				break
			}
			parent = groups[p].parent
		}
	}
	assignActivityFallbacks(groups, byKey, primary, assignments)
	var result []api.CommandActivity
	positions := map[int]int{}
	// written collects the write targets of each row's folded primaries.
	written := map[int][]string{}
	// The row sequential primaries fold into, and the group that opened it.
	activeRow, activeGroup := -1, activityGroup{}
	for i, g := range groups {
		if !primary[i] {
			continue
		}
		if activeRow >= 0 && activityFolds(activeGroup, g) {
			positions[i] = activeRow
			written[activeRow] = append(written[activeRow], activityPaths(g.input.arguments, "write")...)
			continue
		}
		positions[i] = len(result)
		written[len(result)] = activityPaths(g.input.arguments, "write")
		activity := groupActivity(g)
		if activity.Subject == "" {
			activity.Subject = subjects[i]
		}
		result = append(result, activity)
		activeRow, activeGroup = positions[i], g
	}
	for row, paths := range written {
		// Folded writes name every file they wrote, not only the first.
		if distinct := uniqueInOrder(paths); len(distinct) > 1 {
			result[row].Subject = boundedActivityText(pathSummary(distinct))
		}
	}
	for i, g := range groups {
		if owner, ok := assignments[i]; ok {
			appendActivityStep(&result[positions[owner]], g)
		}
	}
	if len(result) == 0 && state != activityProgramRunning && len(groups) > 0 {
		for _, g := range groups {
			if activityPresentation(g.input).role != "container" {
				a := groupActivity(g)
				appendActivityStep(&a, g)
				result = append(result, a)
			}
		}
	}
	for i, g := range groups {
		if !primary[i] || g.input.operation != "program &" {
			continue
		}
		// A launch whose job already ran reports the job's outcome, not its start.
		if a := &result[positions[i]]; a.State == "completed" && len(a.Steps) > 1 {
			a.Outcome = activityStateOutcome(a.State)
		}
	}
	for i := range result {
		a := &result[i]
		if a.State == "failed" {
			a.Outcome = failedActivityOutcome(*a)
		}
	}
	return result
}

// Membership is fixed at recording time: same phase, no launches, no external processes.
func activityFolds(active, g activityGroup) bool {
	ar, gr := activityPresentation(active.input), activityPresentation(g.input)
	if ar.group != gr.group || ar.label == "Run process" || gr.label == "Run process" {
		return false
	}
	return active.input.operation != "program &" && g.input.operation != "program &" &&
		active.input.receipt.JobHandle == "" && g.input.receipt.JobHandle == ""
}

// launchOf finds the & launch whose job recorded g.
func launchOf(g activityGroup, groups []activityGroup, byKey map[activityKey]int) (int, bool) {
	seen := map[activityKey]bool{g.key: true}
	for parent := g.parent; parent != (activityKey{}) && !seen[parent]; {
		seen[parent] = true
		p, ok := byKey[parent]
		if !ok {
			return 0, false
		}
		if parent.launch != "" {
			return p, true
		}
		parent = groups[p].parent
	}
	return 0, false
}

func groupDescendsFrom(g activityGroup, parent activityKey, groups []activityGroup, byKey map[activityKey]int) bool {
	seen := map[activityKey]bool{g.key: true}
	for g.parent != (activityKey{}) && !seen[g.parent] {
		if g.parent == parent {
			return true
		}
		seen[g.parent] = true
		p, ok := byKey[g.parent]
		if !ok {
			return false
		}
		g = groups[p]
	}
	return false
}

func uniqueInOrder(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := values[:0:0]
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

// statementRedirect is the anchored redirect inside g's statement, or -1.
func statementRedirect(groups []activityGroup, primary map[int]bool, g activityGroup) int {
	for j, other := range groups {
		if primary[j] && activityRedirect(other.input) && spanWithin(other.input.span, g.input.span) {
			return j
		}
	}
	return -1
}

// spanWithin reports whether inner lies inside outer in the same source.
func spanWithin(inner, outer api.CommandActivitySpan) bool {
	return inner.SourceRef != "" && inner.SourceRef == outer.SourceRef &&
		outer.StartByte <= inner.StartByte && inner.EndByte <= outer.EndByte && outer.StartByte < outer.EndByte
}
