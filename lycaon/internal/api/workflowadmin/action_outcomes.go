package workflowadmin

import (
	"context"

	"github.com/lycaon/lycaon/internal/actionoutcome"
	"github.com/lycaon/lycaon/internal/usernotice"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *RunControl) commitAction(ctx context.Context, a *actionoutcome.Action, run *wire.WorkflowRun) string {
	switch run.Status {
	case wire.WorkflowRunStatusPaused:
		actionoutcome.Register(ctx, run.SessionID).Resolve(actionoutcome.Result{State: wire.ContinuationStateHeld, Reason: wire.ContinuationReasonWorkflowPaused})
	case wire.WorkflowRunStatusCanceled:
		actionoutcome.Register(ctx, run.SessionID).Resolve(actionoutcome.Result{State: wire.ContinuationStateFinished, Reason: wire.ContinuationReasonWorkflowCancelled})
	case wire.WorkflowRunStatusComplete:
		actionoutcome.Register(ctx, run.SessionID).Resolve(actionoutcome.Result{State: wire.ContinuationStateFinished, Reason: wire.ContinuationReasonWorkflowCompleted})
	}
	results := s.Sessions.Coordinator.Runtime.CoordinatorLoop().Outcomes.For(run.SessionID)
	projectID := run.ProjectID
	publisher := s.eventPublisher
	eventCtx := context.Background()
	catalog := s.responses.Notices
	order := a.Order
	render := func(reason string, kind wire.ContinuationAction) *wire.SessionHostError {
		code := wire.NoticeCodeCoordinatorNotContinued
		h := catalog.HostError(code, usernotice.ContinuationContext(reason, kind))
		return &h
	}
	a.Commit(results, run.SessionID, func(ev wire.ActionContinuation) {
		publisher.PublishContinuation(eventCtx, projectID, ev)
		results.Notices.Observe(order, ev, render, func(n wire.ContinuationNotice) { publisher.PublishContinuationNotice(eventCtx, projectID, n) })
	})
	return a.ID()
}
