package invocation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/pkg/api"
)

type activityCounts struct {
	total, running, waiting, failed, interrupted, refused, uncertain, effects int
}

type activityKey struct {
	node, operation, scope, launch string
}

type activityGroup struct {
	input       activityInput
	key, parent activityKey
	ordinal     int64
	counts      activityCounts
}

// Repetitions are counted in SQL; only the first execution's inputs are decoded.
func readActivityCounts(ctx context.Context, tx *sql.Tx, program string) (groups []activityGroup, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT min(ordinal),count(*),
 sum(state IN ('prepared','running')),sum(state='waiting'),
 sum(state='failed' OR (state='completed' AND COALESCE(status_code,0)<>0)),
 sum(state='interrupted'),sum(state='refused'),
 sum(effect_certainty IN ('unknown','attempted')),sum(effect_certainty='completed')
 FROM invocation_program_executions WHERE program_id=?
 GROUP BY node_id,operation_id,scope_json,CASE WHEN operation_id='program &' THEN id ELSE '' END ORDER BY min(ordinal)`, program)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var g activityGroup
		c := &g.counts
		if err := rows.Scan(&g.ordinal, &c.total, &c.running, &c.waiting, &c.failed, &c.interrupted, &c.refused, &c.uncertain, &c.effects); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

func readActivityGroups(ctx context.Context, tx *sql.Tx, program string) ([]activityGroup, error) {
	groups, err := readActivityCounts(ctx, tx, program)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		g := &groups[i]
		receipts, _, err := readExecutions(ctx, tx, program, g.ordinal-1, 1, executionSelection{})
		if err != nil {
			return nil, err
		}
		if len(receipts) != 1 {
			return nil, fmt.Errorf("activity representative is unavailable")
		}
		g.input, err = readActivityInput(ctx, tx, receipts[0])
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
	state := activityStepState(g.counts, g.input.receipt)
	outcome := activityStateOutcome(state)
	if g.input.operation == "program &" && state == "completed" {
		outcome = "Started"
	}
	return api.CommandActivity{ID: g.input.receipt.ID, Group: rule.group, Phase: rule.phase, Label: rule.label, Subject: activitySubject(g.input), Directory: stringArgument(g.input.arguments, "cwd"), JobHandle: g.input.receipt.JobHandle, State: state, Outcome: outcome, ExecutionIDs: []string{}, Spans: []api.CommandActivitySpan{}, Repetitions: g.counts.total, Steps: []api.CommandActivityStep{}}
}

// Precedence over folded steps decides the row state; membership never does.
var activityStateRank = map[string]int{"unknown": 0, "completed": 0, "failed": 1, "interrupted": 2, "refused": 3, "running": 4, "waiting": 5}

func activityStepState(c activityCounts, receipt api.CommandExecutionReceipt) string {
	switch {
	case c.waiting > 0:
		return "waiting"
	case c.running > 0:
		return "running"
	case c.refused > 0 || receipt.State == "refused":
		return "refused"
	case c.interrupted > 0:
		return "interrupted"
	case c.failed > 0 || receipt.State == "failed" || receipt.RevisionConflict != nil:
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
	state := activityStepState(c, g.input.receipt)
	_, outcome := activityOutcome(g.input.receipt)
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

// Supporting steps follow recorded ancestry; sequential primaries fold by phase.
func composeActivityGroups(groups []activityGroup, settled bool) []api.CommandActivity {
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
	assignments := make(map[int]int, len(groups))
	for i, g := range groups {
		if primary[i] {
			assignments[i] = i
			continue
		}
		if activityPresentation(g.input).role == "container" {
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
	// Failed work with nowhere to fold becomes its own row instead of disappearing.
	for i, g := range groups {
		if _, assigned := assignments[i]; assigned {
			continue
		}
		if g.counts.failed+g.counts.refused+g.counts.interrupted+g.counts.waiting == 0 {
			continue
		}
		if activityPresentation(g.input).role == "container" && g.parent != (activityKey{}) {
			if _, ok := byKey[g.parent]; ok {
				continue
			}
		}
		primary[i] = true
		assignments[i] = i
	}
	var result []api.CommandActivity
	positions := map[int]int{}
	active := -1
	for i, g := range groups {
		if !primary[i] {
			continue
		}
		if active >= 0 && activityFolds(groups[active], g) {
			positions[i] = positions[active]
			continue
		}
		positions[i] = len(result)
		result = append(result, groupActivity(g))
		active = i
	}
	for i, g := range groups {
		if owner, ok := assignments[i]; ok {
			appendActivityStep(&result[positions[owner]], g)
		}
	}
	if len(result) == 0 && settled && len(groups) > 0 {
		for _, g := range groups {
			if activityPresentation(g.input).role != "container" {
				a := groupActivity(g)
				appendActivityStep(&a, g)
				result = append(result, a)
			}
		}
	}
	for i := range result {
		a := &result[i]
		if a.State == "failed" && a.FailedCount < a.ExecutionCount {
			a.Outcome = fmt.Sprintf("Finished · %d steps reported failure", a.FailedCount)
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
