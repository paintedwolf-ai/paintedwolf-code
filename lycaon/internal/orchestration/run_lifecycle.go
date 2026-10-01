package orchestration

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/pkg/api"
)

// Cancel asks the owning workflow and delegation to settle; their terminal state wins.
func (o *OrchestratorImpl) Cancel(ctx context.Context, runID string, reason TerminationReason) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	o.mu.Lock()
	state, ok := o.runs[runID]
	if !ok {
		o.mu.Unlock()
		return fmt.Errorf("run %q not found", runID)
	}
	if !state.cancelRequested {
		state.cancelRequested = true
		state.cancelReason = reason
	}
	reason = state.cancelReason
	delegationID, workflowRunID := state.delegationID, state.workflowRunID
	if delegationID == "" {
		state.active, state.phase = false, string(reason)
	}
	o.mu.Unlock()
	if o.workflows != nil && workflowRunID != "" {
		run, err := o.workflows.Cancel(ctx, workflowRunID, string(reason))
		if err != nil {
			return err
		}
		if run != nil {
			phase, terminal := workflowStatusPhase(run)
			o.mu.Lock()
			state.phase = phase
			if terminal {
				state.active = false
			}
			o.mu.Unlock()
		}
	}
	if o.delegation != nil && delegationID != "" {
		return o.abortRunDelegation(ctx, state, delegationID, reason)
	}
	return nil
}

// bindRunDelegation closes the setup window before any leg can be dispatched.
func (o *OrchestratorImpl) bindRunDelegation(ctx context.Context, state *runState, delegationID string) error {
	o.mu.Lock()
	state.delegationID = delegationID
	reason, canceled := state.cancelReason, state.cancelRequested
	o.mu.Unlock()
	if !canceled {
		return nil
	}
	if err := o.abortRunDelegation(ctx, state, delegationID, reason); err != nil {
		return err
	}
	return context.Canceled
}

func (o *OrchestratorImpl) abortRunDelegation(ctx context.Context, state *runState, delegationID string, reason TerminationReason) error {
	settled, err := o.store.Get(ctx, delegationID)
	if err != nil {
		return err
	}
	if settled.Status == api.DelegationStatusActive {
		if err := o.delegation.Abort(ctx, delegationID, string(reason)); err != nil {
			return err
		}
		settled, err = o.store.Get(ctx, delegationID)
		if err != nil {
			return err
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	// Abort and worker settlement race at the delegation store's atomic transition.
	switch settled.Status {
	case api.DelegationStatusAborted:
		state.active = false
		if state.workflowRunID == "" {
			state.phase = settled.Reason
		}
	case api.DelegationStatusDone, api.DelegationStatusFailed, api.DelegationStatusCanceled:
		state.active = false
		if state.workflowRunID == "" {
			state.phase = "done"
		}
	case api.DelegationStatusActive:
		return fmt.Errorf("delegation %q remained active after cancellation", delegationID)
	}
	return nil
}

// workflowStatusPhase keeps a terminal workflow from presenting an executable phase.
func workflowStatusPhase(run *api.WorkflowRun) (phase string, terminal bool) {
	switch run.Status {
	case api.WorkflowRunStatusComplete, api.WorkflowRunStatusFailed, api.WorkflowRunStatusCanceled, api.WorkflowRunStatusInterrupted:
		return string(run.Status), true
	case api.WorkflowRunStatusRunning, api.WorkflowRunStatusPaused, api.WorkflowRunStatusPausedOnChild:
		return run.CurrentPhase, false
	}
	return run.CurrentPhase, false
}
