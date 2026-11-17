package orchestration

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/pkg/api"
)

// Cancel asks the owning workflow and worker batch to settle; their terminal state wins.
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
	batchID, workflowRunID := state.batchID, state.workflowRunID
	if batchID == "" {
		state.active, state.phase = false, string(reason)
	}
	o.mu.Unlock()
	if o.workflows != nil && workflowRunID != "" {
		run, err := o.workflows.Controls.Cancel(ctx, workflowRunID, string(reason))
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
	if o.workers != nil && batchID != "" {
		return o.abortRunBatch(ctx, state, batchID, reason)
	}
	return nil
}

func (o *OrchestratorImpl) bindRunBatch(ctx context.Context, state *runState, batchID string) error {
	o.mu.Lock()
	state.batchID = batchID
	reason, canceled := state.cancelReason, state.cancelRequested
	o.mu.Unlock()
	if !canceled {
		return nil
	}
	if err := o.abortRunBatch(ctx, state, batchID, reason); err != nil {
		return err
	}
	return context.Canceled
}

func (o *OrchestratorImpl) abortRunBatch(ctx context.Context, state *runState, batchID string, reason TerminationReason) error {
	settled, err := o.workers.GetBatch(ctx, batchID)
	if err != nil {
		return err
	}
	if settled.Status == TopologyBatchActive {
		if err := o.workers.AbortBatch(ctx, batchID, string(reason)); err != nil {
			return err
		}
		settled, err = o.workers.GetBatch(ctx, batchID)
		if err != nil {
			return err
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	switch settled.Status {
	case TopologyBatchAborted:
		state.active = false
		if state.workflowRunID == "" {
			state.phase = settled.Reason
		}
	case TopologyBatchDone, TopologyBatchFailed:
		state.active = false
		if state.workflowRunID == "" {
			state.phase = "done"
		}
	case TopologyBatchActive:
		return fmt.Errorf("topology batch %q remained active after cancellation", batchID)
	}
	return nil
}

func workflowStatusPhase(run *api.WorkflowRun) (phase string, terminal bool) {
	switch run.Status {
	case api.WorkflowRunStatusComplete, api.WorkflowRunStatusFailed, api.WorkflowRunStatusCanceled, api.WorkflowRunStatusInterrupted:
		return string(run.Status), true
	case api.WorkflowRunStatusRunning, api.WorkflowRunStatusPaused, api.WorkflowRunStatusPausedOnChild:
		return run.CurrentPhase, false
	}
	return run.CurrentPhase, false
}
