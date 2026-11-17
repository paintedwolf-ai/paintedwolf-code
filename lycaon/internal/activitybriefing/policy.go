package activitybriefing

import (
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Row names how an activity was classified; the outcome section records it
// so the evidence page can say what kind of activity was explained.
type Row string

const (
	RowTool       Row = "tool_activity"
	RowExecute    Row = "command_execute"
	RowFailed     Row = "command_failed_or_interrupted"
	RowMixedSteps Row = "command_steps_differ"
	RowInspect    Row = "command_inspect_or_mutate"
	RowRefused    Row = "command_refused"
)

// changeMode says how an activity's change section is produced.
type changeMode int

const (
	changeNone changeMode = iota
	changeGenerated
	// changeNothingRan states that a refused activity ran nothing.
	changeNothingRan
)

// decision is what the policy asks for one opened activity. Purpose is
// always generated; the change waits until the activity settles.
type decision struct {
	row    Row
	change changeMode
}

// facts are the typed host facts the policy reads; never command text or
// output prose.
type facts struct {
	command    bool
	activities []wire.CommandActivity
	settled    bool
}

func (f facts) anyState(states ...string) bool {
	for _, activity := range f.activities {
		for _, state := range states {
			if activity.State == state {
				return true
			}
		}
	}
	return false
}

func (f facts) allState(state string) bool {
	for _, activity := range f.activities {
		if activity.State != state {
			return false
		}
	}
	return len(f.activities) > 0
}

func (f facts) phase(phase string) bool {
	for _, activity := range f.activities {
		if activity.Phase == phase {
			return true
		}
	}
	return false
}

// stepsDiffer reports a folded card whose settled steps ended differently.
func (f facts) stepsDiffer() bool {
	seen := map[string]bool{}
	for _, activity := range f.activities {
		for _, step := range activity.Steps {
			seen[step.State] = true
		}
	}
	return len(seen) > 1
}

// decide applies the activity generation policy.
func decide(f facts) decision {
	settledChange := changeNone
	if f.settled {
		settledChange = changeGenerated
	}
	if !f.command {
		return decision{row: RowTool, change: settledChange}
	}
	switch {
	case f.allState("refused"):
		return decision{row: RowRefused, change: changeNothingRan}
	case f.anyState("failed", "interrupted"):
		return decision{row: RowFailed, change: settledChange}
	case f.phase("execute"):
		return decision{row: RowExecute, change: settledChange}
	case f.settled && f.stepsDiffer():
		return decision{row: RowMixedSteps, change: settledChange}
	default:
		return decision{row: RowInspect, change: settledChange}
	}
}
