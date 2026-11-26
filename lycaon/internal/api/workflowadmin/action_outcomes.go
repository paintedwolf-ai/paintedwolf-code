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
	a.Commit(results, run.SessionID, func(ev wire.ActionContinuation) { publisher.PublishContinuation(ctx, projectID, ev) })
	return a.ID()
}
