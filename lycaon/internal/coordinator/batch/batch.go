package batch

import (
	"strings"
)

// Coordinator batch lifecycle phases (scaffold vars coordinator_batch.phase).
const (
	PhasePreDispatch = "pre_dispatch"
	PhaseDispatch    = "dispatch"
	PhaseIntegrate   = "integrate"
	PhaseSynthesize  = "synthesize"
	PhaseClosed      = "closed"
)

// Event is a host-managed batch phase transition trigger.
type Event int

const (
	EventVisibleUserMessage Event = iota
	EventWriterTaskEnqueued
	EventOverlaysPendingIdle
	EventSynthesisReady
	EventGroundedSynthesisAccepted
)

// State is the live batch phase + epoch read from scaffold vars.
type State struct {
	Phase string
	Seq   int
}

func targetPhaseForEvent(ev Event) string {
	switch ev {
	case EventVisibleUserMessage:
		return PhasePreDispatch
	case EventWriterTaskEnqueued:
		return PhaseDispatch
	case EventOverlaysPendingIdle:
		return PhaseIntegrate
	case EventSynthesisReady:
		return PhaseSynthesize
	case EventGroundedSynthesisAccepted:
		return PhaseClosed
	default:
		return ""
	}
}

func phaseRank(phase string) int {
	switch strings.TrimSpace(strings.ToLower(phase)) {
	case "", PhasePreDispatch:
		return 1
	case PhaseDispatch:
		return 2
	case PhaseIntegrate:
		return 3
	case PhaseSynthesize:
		return 4
	case PhaseClosed:
		return 5
	default:
		return 0
	}
}

// Read returns batch phase state from scaffold vars.
func Read(vars map[string]any) State {
	cb, _ := vars["coordinator_batch"].(map[string]any)
	if cb == nil {
		return State{Phase: PhasePreDispatch, Seq: 0}
	}
	phase, _ := cb["phase"].(string)
	phase = strings.TrimSpace(phase)
	if phase == "" {
		phase = PhasePreDispatch
	}
	return State{Phase: phase, Seq: seqInt(cb["seq"])}
}

func seqInt(raw any) int {
	switch v := raw.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func cloneVars(vars map[string]any) map[string]any {
	if vars == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(vars))
	for k, v := range vars {
		out[k] = v
	}
	return out
}

func write(vars map[string]any, state State) map[string]any {
	vars = cloneVars(vars)
	vars["coordinator_batch"] = map[string]any{
		"phase": strings.TrimSpace(state.Phase),
		"seq":   state.Seq,
	}
	return vars
}

// ApplyTransition mutates vars for one batch event. eventSeq fences stale events.
func ApplyTransition(vars map[string]any, ev Event, eventSeq int) (map[string]any, State, bool) {
	cur := Read(vars)
	target := targetPhaseForEvent(ev)
	if target == "" {
		return vars, cur, false
	}

	if ev == EventVisibleUserMessage {
		next := State{Phase: PhasePreDispatch, Seq: cur.Seq + 1}
		return write(vars, next), next, true
	}

	if eventSeq > 0 && eventSeq < cur.Seq {
		return vars, cur, false
	}

	targetRank := phaseRank(target)
	curRank := phaseRank(cur.Phase)
	if targetRank <= curRank {
		return vars, cur, false
	}

	next := State{Phase: target, Seq: cur.Seq}
	return write(vars, next), next, true
}
