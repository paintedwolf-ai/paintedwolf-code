package surface

import (
	"strings"

	"github.com/lycaon/lycaon/internal/spawn"
)

const (
	ExecutionModeFamilyInvestigate = "investigate"
	ExecutionModeFamilyOrchestrate = "orchestrate"
	ExecutionModeFamilyWrapup      = "wrapup"
)

// ModeTransitionCauseKind identifies who pushed an explicit mode entry cause.
type ModeTransitionCauseKind string

const (
	ModeTransitionCauseWorkflowDefault ModeTransitionCauseKind = "workflow_default"
	ModeTransitionCausePhaseHook       ModeTransitionCauseKind = "phase_hook"
)

// ModeTransitionCause is an explicit execution-mode entry pushed before assembly.
type ModeTransitionCause struct {
	Kind ModeTransitionCauseKind
	Mode string // investigate | orchestrate | wrapup
}

// TransitionVars are pongo execution-mode visibility classes for one coordinator turn.
type TransitionVars struct {
	ExecutionMode         string
	ExecutionModePrevious string
	ExecutionModeEntered  string
	ExecutionModeLeft     string
}

// ExecutionModeState holds persisted prior-family memory for transition detection.
type ExecutionModeState struct {
	LastFamily string // "", investigate, orchestrate
}

// ExecutionModeFamily maps a coordinator surface id to a steady execution-mode family.
func ExecutionModeFamily(surfaceID string) string {
	switch strings.TrimSpace(surfaceID) {
	case "implement_investigate":
		return ExecutionModeFamilyInvestigate
	case spawn.SurfaceImplementSynthesis:
		return ExecutionModeFamilyWrapup
	case spawn.SurfaceImplementRouting, spawn.SurfaceImplementDispatch,
		SurfaceImplementOverlayPromote, SurfaceImplementPark:
		return ExecutionModeFamilyOrchestrate
	default:
		return ""
	}
}

// ComputeModeTransition derives transition variables from mode families and causes.
func ComputeModeTransition(previous, current string, causes []ModeTransitionCause) TransitionVars {
	previous = strings.TrimSpace(previous)
	current = strings.TrimSpace(current)

	if len(causes) > 0 {
		entered := current
		if mode := strings.TrimSpace(causes[0].Mode); mode != "" {
			entered = mode
		}
		return TransitionVars{
			ExecutionMode:         current,
			ExecutionModePrevious: previous,
			ExecutionModeEntered:  entered,
			ExecutionModeLeft:     "",
		}
	}
	if previous == "" && current == "" {
		return TransitionVars{}
	}
	if previous == "" {
		return TransitionVars{
			ExecutionMode:         current,
			ExecutionModePrevious: "",
		}
	}
	if previous != "" && current != "" && previous != current {
		return TransitionVars{
			ExecutionMode:         current,
			ExecutionModePrevious: previous,
			ExecutionModeEntered:  current,
			ExecutionModeLeft:     previous,
		}
	}
	if current == "" && previous != "" {
		return TransitionVars{
			ExecutionMode:         "",
			ExecutionModePrevious: previous,
			ExecutionModeLeft:     previous,
		}
	}
	return TransitionVars{
		ExecutionMode:         current,
		ExecutionModePrevious: previous,
	}
}
