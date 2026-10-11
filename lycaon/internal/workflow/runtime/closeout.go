package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/pkg/api"
)

// ActiveCloseoutGateState excludes explicit waits for human input.
func (m *SessionPolicy) ActiveCloseoutGateState(ctx context.Context, sessionID string) (workflowfacts.WorkflowCloseoutGateState, error) {
	var state workflowfacts.WorkflowCloseoutGateState
	if m == nil || m.Runs == nil {
		return state, fmt.Errorf("workflow closeout state unavailable")
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil || active.Status != api.WorkflowRunStatusRunning {
		return state, err
	}
	manifest, err := m.Resolver.ForRun(ctx, active)
	if err != nil {
		return state, err
	}
	def, ok := manifest.PhaseByID(active.CurrentPhase)
	if !ok {
		return state, fmt.Errorf("workflow closeout phase %s unavailable", active.CurrentPhase)
	}
	if !def.Closeout.Gated() {
		return state, nil
	}
	vars, err := m.Runs.GetScaffoldVars(ctx, active.ID)
	if err != nil || scaffoldvars.HasPendingUserInput(vars) {
		return state, err
	}
	met, result, err := m.gateEvaluator().PhaseGateMet(ctx, manifest, active, vars)
	if err != nil || met {
		return state, err
	}
	state.Gated = true
	state.Phase = strings.TrimSpace(active.CurrentPhase)
	state.OpenLeaves = append([]string(nil), result.FailedLeaves...)
	if len(state.OpenLeaves) == 0 {
		state.OpenLeaves = []string{strings.TrimSpace(def.CompleteWhen)}
	}
	return state, nil
}

// AssertSessionCompletion distinguishes open obligations from a completed turn.
func (m *SessionPolicy) AssertSessionCompletion(ctx context.Context, sessionID string) error {
	state, err := m.ActiveCloseoutGateState(ctx, sessionID)
	if err != nil {
		return err
	}
	if state.Gated {
		return &workflowfacts.IncompleteCloseoutError{State: state}
	}
	return nil
}
