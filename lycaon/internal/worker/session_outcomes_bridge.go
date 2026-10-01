package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/pkg/api"
)

// SessionOutcomeBridge wakes the session loop after a worker completes or fails.
type SessionOutcomeBridge struct {
	Sessions WorkerSessionOutcomes
	Inner    OutcomeProjection
}

// OnWorkerComplete projects the result before waking its parent.
func (b *SessionOutcomeBridge) OnWorkerComplete(ctx context.Context, jobID string, result api.WorkerResult) error {
	if b.Sessions == nil {
		if b.Inner == nil {
			return nil
		}
		return b.Inner.OnWorkerComplete(ctx, jobID, result)
	}
	task, ok := b.Sessions.WorkerTaskByID(jobID)
	parentID := strings.TrimSpace(task.ParentSessionID)
	status := strings.TrimSpace(result.Status)
	var errs []error
	if ok && parentID != "" {
		if status == "" {
			status = "complete"
		}
		projectedStatus, err := b.Sessions.ProjectWorkerResult(ctx, task, result)
		if err != nil {
			return fmt.Errorf("project worker result: %w", err)
		} else if strings.TrimSpace(projectedStatus) != "" {
			status = projectedStatus
		}
		if err := b.Sessions.RecordWorkerTerminalProofAfterQueueComplete(ctx, parentID, jobID, status); err != nil {
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
	if b.Sessions == nil {
		if b.Inner == nil {
			return nil
		}
		return b.Inner.OnWorkerFailed(ctx, jobID, err)
	}
	task, ok := b.Sessions.WorkerTaskByID(jobID)
	if !ok {
		return errors.Join(errs...)
	}
	parentID := strings.TrimSpace(task.ParentSessionID)
	if parentID == "" {
		return errors.Join(errs...)
	}
	if projectErr := b.Sessions.ProjectWorkerFailure(ctx, task, err); projectErr != nil {
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
	if b.Sessions != nil && strings.TrimSpace(task.ParentSessionID) != "" {
		b.Sessions.NotifyWorkerCycleTerminal(ctx, task.ParentSessionID, task.ID)
	}
}

func (b *SessionOutcomeBridge) scheduleParentWake(ctx context.Context, task session.WorkerSummaryInput, jobID, status string) {
	parentID := strings.TrimSpace(task.ParentSessionID)
	if parentID == "" || b.Sessions == nil {
		return
	}
	if api.WorkerSummaryLegSucceeded(api.WorkerSummaryStatus(status)) {
		b.Sessions.ArmProgressClosure(ctx, parentID, jobID)
	}
	if strings.TrimSpace(task.LegID) != "" || strings.TrimSpace(task.DelegationID) != "" {
		b.Sessions.NudgeLegFinishedLoopWake(ctx, parentID, time.Now().UTC(), task.LegID)
		return
	}
	if !api.WorkerResultStatusReactable(status) {
		return
	}
	if !b.Sessions.ShouldNudgeCoordinatorLoopAfterWorkerTask(ctx, parentID, task.ProjectID, jobID) {
		return
	}
	b.Sessions.NudgeCoordinatorLoopAfterWorkerJobTerminal(
		ctx,
		parentID,
		jobID,
		b.Sessions.CoordinatorEnvelopeForWorkerCycleTerminal(ctx, parentID, jobID),
	)
}

// WorkerSessionOutcomes schedules coordinator loop wakes after a worker completes or fails.
type WorkerSessionOutcomes interface {
	WorkerTaskByID(jobID string) (session.WorkerSummaryInput, bool)
	ProjectWorkerResult(ctx context.Context, task session.WorkerSummaryInput, result api.WorkerResult) (string, error)
	ProjectWorkerFailure(ctx context.Context, task session.WorkerSummaryInput, failure error) error
	RecordWorkerTerminalProofAfterQueueComplete(ctx context.Context, parentID, jobID, status string) error
	NudgeLegFinishedLoopWake(ctx context.Context, parentID string, completedAt time.Time, legID string)
	NotifyWorkerCycleTerminal(ctx context.Context, parentID, completingJobID string)
	ShouldNudgeCoordinatorLoopAfterWorkerTask(ctx context.Context, parentID, projectDir, completingJobID string) bool
	NudgeCoordinatorLoopAfterWorkerJobTerminal(ctx context.Context, parentID, completingJobID string, env anchor.Envelope)
	CoordinatorEnvelopeForWorkerCycleTerminal(ctx context.Context, sessionID, completingJobID string) anchor.Envelope
	ArmProgressClosure(ctx context.Context, rootID, jobID string)
}
