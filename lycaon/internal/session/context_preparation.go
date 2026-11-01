package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/session/contextpreparation"
	"github.com/lycaon/lycaon/pkg/api"
)

func newContextPreparation(host *Host) *contextpreparation.Service {
	return contextpreparation.New(func(ctx context.Context, id string) (contextpreparation.Candidate, error) {
		status, pending, err := host.Runner.SubmissionState.SessionState(ctx, id)
		if err != nil || pending || status != api.SessionStatusIdle {
			return contextpreparation.Candidate{}, err
		}
		return host.Coordinator.BuildPreparation(ctx, id)
	})
}
