package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/pkg/api"
)

// SessionOutcomeBridge wakes the session loop after a worker completes or fails.
type SessionOutcomeBridge struct {
	Workers WorkerTerminalEvents
	Loop    WorkerLegWakes
	Results WorkerResultProjection
	State   WorkerCycleWakeState
	Closure ProgressClosureArm
	Inner   OutcomeProjection
}

// OnWorkerComplete projects the result before waking its parent.
func (b *SessionOutcomeBridge) OnWorkerComplete(ctx context.Context, jobID string, result api.WorkerResult) error {
	if b.Results == nil {
		if b.Inner == nil {
			return nil
		}
		return b.Inner.OnWorkerComplete(ctx, jobID, result)
	}
	task, ok := b.Results.TaskByID(jobID)
	parentID := strings.TrimSpace(task.ParentSessionID)
	status := strings.TrimSpace(result.Status)
	var errs []error
	if ok && parentID != "" {
		if status == "" {
			status = "complete"
		}
		projectedStatus, err := b.Results.ProjectResult(ctx, task, result)
		if err != nil {
			return fmt.Errorf("project worker result: %w", err)
		} else if strings.TrimSpace(projectedStatus) != "" {
			status = projectedStatus
		}
		if err := b.Results.RecordTerminalProof(ctx, parentID, jobID, status); err != nil {
			return fmt.Errorf("record worker terminal proof: %w", err)
		}
	}
	if b.Inner != nil {
		if err := b.Inner.OnWorkerComplete(ctx, jobID, result); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 || !ok || parentID == "" {
		return errors.Join(errs...)
	}
	b.scheduleParentWake(ctx, task, jobID, status)
	return errors.Join(errs...)
}

// OnWorkerFailed applies the same projection-before-wake ordering.
func (b *SessionOutcomeBridge) OnWorkerFailed(ctx context.Context, jobID string, err error) error {
	var errs []error
	if b.Results == nil {
		if b.Inner == nil {
			return nil
		}
		return b.Inner.OnWorkerFailed(ctx, jobID, err)
	}
	task, ok := b.Results.TaskByID(jobID)
	if !ok {
		return errors.Join(errs...)
	}
	parentID := strings.TrimSpace(task.ParentSessionID)
	if parentID == "" {
		return errors.Join(errs...)
	}
	if projectErr := b.Results.ProjectFailure(ctx, task, err); projectErr != nil {
		return fmt.Errorf("project worker failure: %w", projectErr)
	}
	if b.Inner != nil {
		if innerErr := b.Inner.OnWorkerFailed(ctx, jobID, err); innerErr != nil {
			errs = append(errs, innerErr)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	// Explicit cancellation does not wake the coordinator.
	if !errors.Is(err, context.Canceled) {
		b.scheduleParentWake(ctx, task, jobID, "failed")
	}
	return errors.Join(errs...)
}

// OnOutcomeDelivered releases parent deferrals after the queue acknowledgement is visible.
func (b *SessionOutcomeBridge) OnOutcomeDelivered(ctx context.Context, task api.WorkerTask) {
	if b.Workers != nil && strings.TrimSpace(task.ParentSessionID) != "" {
		b.Workers.Terminal(ctx, task.ParentSessionID, task.ID)
	}
}

func (b *SessionOutcomeBridge) scheduleParentWake(ctx context.Context, task workeroutcomes.SummaryInput, jobID, status string) {
	parentID := strings.TrimSpace(task.ParentSessionID)
	if parentID == "" || b.Workers == nil {
		return
	}
	if api.WorkerSummaryLegSucceeded(api.WorkerSummaryStatus(status)) {
		b.Closure.Arm(ctx, parentID, jobID)
	}
	if (strings.TrimSpace(task.LegID) != "" || strings.TrimSpace(task.DelegationID) != "") && b.Loop != nil {
		b.Loop.NudgeLegFinished(ctx, parentID, time.Now().UTC(), task.LegID)
		return
	}
	if !api.WorkerResultStatusReactable(status) {
		return
	}
	if !b.State.ShouldNudge(ctx, parentID, task.ProjectID, jobID) {
		return
	}
	b.Workers.AfterTerminal(
		ctx,
		parentID,
		jobID,
		b.Results.EnvelopeForTerminal(ctx, parentID, jobID),
	)
}

type WorkerTerminalEvents interface {
	Terminal(context.Context, string, string)
	AfterTerminal(context.Context, string, string, anchor.Envelope)
}
type WorkerLegWakes interface {
	NudgeLegFinished(context.Context, string, time.Time, string)
}
