package workflowadmin

import (
	"context"
	"github.com/lycaon/lycaon/internal/actionoutcome"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *RunControl) commitAction(ctx context.Context, a *actionoutcome.Action, run *wire.WorkflowRun) string {
	results := s.Sessions.Coordinator.Runtime.CoordinatorLoop().Outcomes.For(run.SessionID)
	projectID := run.ProjectID
	publisher := s.eventPublisher
	ctx = context.WithoutCancel(ctx)
	order := a.Order
	render := func(reason string, kind wire.ContinuationAction) *wire.SessionHostError {
		code := wire.NoticeCodeCoordinatorNotContinued
		copy := s.responses.Notices.RenderWire(string(code), map[string]any{"reason": reason, "action": string(kind)})
		h := wire.NewSessionHostError(code)
		h.Title = copy.Title
		h.Message = copy.Message
		h.Scope = wire.NoticeScopeSession
		h.Tier = wire.NoticeTierNonCatastrophic
		for _, id := range copy.Actions {
			h.Actions = append(h.Actions, wire.NoticeAction(id))
		}
		return &h
	}
	a.Commit(results, run.SessionID, func(ev wire.ActionContinuation) {
		publisher.PublishContinuation(ctx, projectID, ev)
		results.Notices.Observe(order, ev, render, func(n wire.ContinuationNotice) { publisher.PublishContinuationNotice(ctx, projectID, n) })
	})
	return a.ID()
}
