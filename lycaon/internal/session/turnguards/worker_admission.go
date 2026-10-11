package turnguards

import (
	"context"

	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
)

func (m *Service) WorkerAdmission() workeradmission.WorkerCycleGuardDeps {
	if m == nil {
		return workeradmission.WorkerCycleGuardDeps{}
	}
	return workeradmission.WorkerCycleGuardDeps{
		Workers: m.workerQueue,
		MaxWorkers: func(ctx context.Context, sessionID string) int {
			if m.workflows == nil {
				return 0
			}
			return m.workflows.Ambient.ParallelTaskMaxWorkers(ctx, sessionID)
		},
		MaxReadWorkers: func(ctx context.Context, sessionID string) int {
			if m.workflows == nil {
				return 0
			}
			return m.workflows.Ambient.ParallelTaskMaxReadWorkers(ctx, sessionID)
		},
		MaxWriteWorkers: func(ctx context.Context, sessionID string) int {
			if m.workflows == nil {
				return 0
			}
			return m.workflows.Ambient.ParallelTaskMaxWriteWorkers(ctx, sessionID)
		},
		PhaseGuardState: func(ctx context.Context, sessionID string) workflowfacts.WorkflowPhaseGuardState {
			if m.workflows == nil {
				return workflowfacts.WorkflowPhaseGuardState{}
			}
			return m.workflows.Policy.ActivePhaseGuardState(ctx, sessionID)
		},
		RepoKnownEmpty: func(ctx context.Context, workspacePath string) bool {
			return repoinfo.MeasuredEmpty(ctx, m.repoProvider, workspacePath)
		},
	}
}
