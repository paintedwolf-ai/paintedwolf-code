package activitybriefing

import (
	"github.com/lycaon/lycaon/internal/briefing"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Row names the generation policy row an activity fell under; the outcome
// section records it so the evidence page can say why.
type Row string

const (
	RowTool        Row = "tool_activity"
	RowExecute     Row = "command_execute"
	RowFailed      Row = "command_failed_or_interrupted"
	RowMixedSteps  Row = "command_steps_differ"
	RowLookup      Row = "command_settled_inspect_or_mutate"
	RowRefused     Row = "command_refused"
	RowUnsupported Row = "command_running_inspect_or_mutate"
)

// decision is what the policy asks of the model for one activity.
type decision struct {
	row     Row
	purpose briefing.SectionMode
	change  bool
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
	if !f.command {
		return decision{row: RowTool, purpose: briefing.Generated, change: f.settled}
	}
	switch {
	case f.allState("refused"):
		return decision{row: RowRefused, purpose: briefing.Lookup}
	case f.anyState("failed", "interrupted"):
		return decision{row: RowFailed, purpose: briefing.Generated, change: f.settled}
	case f.phase("execute"):
		return decision{row: RowExecute, purpose: briefing.Generated, change: f.settled}
	case f.settled && f.stepsDiffer():
		return decision{row: RowMixedSteps, purpose: briefing.Generated, change: true}
	case !f.settled:
		return decision{row: RowUnsupported, purpose: briefing.Lookup}
	default:
		return decision{row: RowLookup, purpose: briefing.Lookup}
	}
}
