package invocation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/lycaon/lycaon/pkg/api"
)

type activityCounts struct {
	total, running, waiting, failed, interrupted, refused, uncertain, effects int
}

type activityGroup struct {
	input       activityInput
	key, parent string
	ordinal     int64
	counts      activityCounts
}

func activityGroupKey(node, operation, scope string) string {
	value, _ := json.Marshal([]string{node, operation, scope})
	return string(value)
}

// Repetitions are counted in SQL; only the first execution's inputs are decoded.
func readActivityGroups(ctx context.Context, tx *sql.Tx, program string) ([]activityGroup, error) {
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
	var groups []activityGroup
	for rows.Next() {
		var g activityGroup
		c := &g.counts
		if err := rows.Scan(&g.ordinal, &c.total, &c.running, &c.waiting, &c.failed, &c.interrupted, &c.refused, &c.uncertain, &c.effects); err != nil {
			_ = rows.Close()
			return nil, err
		}
		groups = append(groups, g)
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
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
		g.key = activityGroupKey(receipts[0].NodeID, g.input.operation, g.input.scope)
		if g.input.operation == "program &" {
			g.key = receipts[0].ID
		}
		var node, operation, scope string
		err = tx.QueryRowContext(ctx, `SELECT node_id,operation_id,scope_json FROM invocation_program_executions WHERE program_id=? AND id=?`, program, receipts[0].ParentExecutionID).Scan(&node, &operation, &scope)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if err == nil {
			g.parent = activityGroupKey(node, operation, scope)
			if operation == "program &" {
				g.parent = receipts[0].ParentExecutionID
			}
		}
	}
	return groups, nil
}

func groupActivity(g activityGroup) api.CommandActivity {
	rule := activityPresentation(g.input)
	state, outcome := activityOutcome(g.input.receipt)
	if g.counts.waiting > 0 {
		state, outcome = "waiting", "Waiting"
	} else if g.counts.running > 0 {
		state, outcome = "running", "Running"
	} else if g.counts.refused > 0 {
		state, outcome = "refused", "Refused"
	} else if g.counts.interrupted > 0 {
		state, outcome = "interrupted", "Interrupted"
	} else if g.counts.failed > 0 {
		state, outcome = "failed", "Failed"
	} else {
		state, outcome = "completed", "Finished"
	}
	if g.input.operation == "program &" && state == "completed" {
		outcome = "Started"
	}
	return api.CommandActivity{ID: g.input.receipt.ID, Group: rule.group, Label: rule.label, Subject: activitySubject(g.input), Directory: stringArgument(g.input.arguments, "cwd"), JobHandle: g.input.receipt.JobHandle, State: state, Outcome: outcome, ExecutionIDs: []string{}, Spans: []api.CommandActivitySpan{}, Repetitions: g.counts.total, Steps: []api.CommandActivityStep{}}
}

func appendActivityStep(a *api.CommandActivity, g activityGroup) {
	c := g.counts
	state, outcome := activityOutcome(g.input.receipt)
	a.ExecutionCount += c.total
	a.FailedCount += c.failed + c.refused + c.interrupted
	a.WaitingCount += c.waiting
	a.UncertainEffects += c.uncertain
	a.CompletedEffects += c.effects
	a.ExecutionIDs = append(a.ExecutionIDs, g.input.receipt.ID)
	a.Spans = append(a.Spans, g.input.span)
	a.Steps = append(a.Steps, api.CommandActivityStep{ID: g.input.receipt.ID, Command: boundedActivityText(g.input.receipt.Command), Count: c.total, Running: c.running, Waiting: c.waiting, Failed: c.failed, Refused: c.refused, Interrupted: c.interrupted, State: state, Outcome: outcome})
	if a.State == "completed" && c.running > 0 {
		a.State, a.Outcome = "running", "Running"
	}
	if c.waiting > 0 {
		a.State, a.Outcome = "waiting", "Waiting"
	}
}

// Supporting steps follow recorded ancestry, independent of execution start order.
func composeActivityGroups(groups []activityGroup, settled bool) []api.CommandActivity {
	byKey := make(map[string]int, len(groups))
	for i, g := range groups {
		byKey[g.key] = i
	}
	primary := make(map[int]bool)
	for i, g := range groups {
		role := activityPresentation(g.input).role
		if role == "primary" || (role == "context" && activityNeedsAttention(g.input.receipt)) {
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
		seen := map[string]bool{g.key: true}
		for parent != "" && !seen[parent] {
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
	for i, g := range groups {
		if _, assigned := assignments[i]; assigned {
			continue
		}
		if activityPresentation(g.input).role == "container" {
			continue
		}
		if g.counts.failed+g.counts.refused+g.counts.interrupted+g.counts.waiting > 0 && (g.parent == "" || g.parent == "root") {
			primary[i] = true
			assignments[i] = i
		}
	}
	var result []api.CommandActivity
	positions := map[int]int{}
	for i, g := range groups {
		if !primary[i] {
			continue
		}
		positions[i] = len(result)
		result = append(result, groupActivity(g))
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
		if a.State == "completed" && a.FailedCount > 0 {
			a.Outcome = fmt.Sprintf("Finished · %d steps reported failure", a.FailedCount)
		}
	}
	return result
}

func groupDescendsFrom(g activityGroup, parent string, groups []activityGroup, byKey map[string]int) bool {
	seen := map[string]bool{g.key: true}
	for g.parent != "" && !seen[g.parent] {
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
