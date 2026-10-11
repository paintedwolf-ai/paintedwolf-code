package worker

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

// deliverPendingOutcomes retries unacknowledged side effects independently.
func (p *LocalWorkerPoller) deliverPendingOutcomes(ctx context.Context) error {
	if p.Queue == nil {
		return nil
	}
	tasks, err := p.Queue.ListPendingOutcomes(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for i := range tasks {
		if err := p.deliverOutcome(ctx, tasks[i]); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// deliverOutcome projects and acknowledges a committed outcome.
func (p *LocalWorkerPoller) deliverOutcome(ctx context.Context, task api.WorkerTask) error {
	// A peer must observe earlier delivery acknowledgements before evaluating the cycle.
	key := task.ParentSessionID
	if key == "" {
		key = task.ID
	}
	lock := p.outcomeLocks.Acquire(key)
	lock.Lock()
	defer lock.Unlock()

	if p.Outcomes == nil {
		return errors.New("outcome recorder required")
	}
	switch task.Status {
	case api.WorkerStatusComplete, api.WorkerStatusHeld:
		result := api.WorkerResult{Status: "complete"}
		if task.Result != nil {
			result = *task.Result
		}
		if err := p.Outcomes.OnWorkerComplete(ctx, task.ID, result); err != nil {
			return fmt.Errorf("deliver completion for %s: %w", task.ID, err)
		}
	case api.WorkerStatusFailed:
		if err := p.Outcomes.OnWorkerFailed(ctx, task.ID, errors.New(strings.TrimSpace(task.Error))); err != nil {
			return fmt.Errorf("deliver failure for %s: %w", task.ID, err)
		}
	case api.WorkerStatusCanceled:
		result := api.WorkerResult{Status: "canceled", Response: "canceled"}
		if task.Result != nil {
			result = *task.Result
			result.Status = "canceled"
		}
		if err := p.Outcomes.OnWorkerComplete(ctx, task.ID, result); err != nil {
			return fmt.Errorf("deliver cancellation for %s: %w", task.ID, err)
		}
	default:
		return nil
	}
	if err := p.Queue.MarkOutcomeDelivered(ctx, task.ID); err != nil {
		return fmt.Errorf("acknowledge outcome for %s: %w", task.ID, err)
	}
	p.Outcomes.OnOutcomeDelivered(ctx, task)
	if p.ParentWaiter != nil {
		p.ParentWaiter.NotifyParent(task.ParentSessionID)
	}
	return nil
}
