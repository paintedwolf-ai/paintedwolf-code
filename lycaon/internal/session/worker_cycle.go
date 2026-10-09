package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/session/projectcontrol"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkerCycleLister loads worker jobs for coordinator cycle guards and outcome bridging.
type WorkerCycleLister interface {
	ListBySession(ctx context.Context, projectID, sessionID string, status ...api.WorkerStatus) ([]api.WorkerTask, error)
	ListPendingOutcomes(ctx context.Context) ([]api.WorkerTask, error)
	Get(jobID string) (*api.WorkerTask, bool)
	ClaimWorkerBranch(ctx context.Context, jobID string) (*api.WorkerTask, error)
}

// workeradmission.CoordinatorTaskConcurrencyCap returns the max pending or running task() jobs for a parent session.
// SetWorkerQueue wires the worker job ledger for coordinator worker-cycle guards.
func (m *Host) SetWorkerQueue(q WorkerCycleLister) {
	if m != nil {
		m.Coordinator.Tools.Workers = q
		m.Coordinator.Nudging.Workers = q
		m.Coordinator.Loop.Workers = q

		m.RewindRuntime.Workers = q
		m.Observations.SetWorkers(q)
		if projects, ok := q.(projectcontrol.Workers); ok {
			m.ProjectControl.SetWorkers(projects)
		}
		m.Coordinator.Guards.SetWorkers(q)
		m.Verification.Evidence.SetWorkers(q)
		m.Runner.Settlement.SetWorkers(q)
		m.Coordinator.Batch.SetWorkers(q)
		m.Coordinator.Nudges.SetTasks(q)
		m.Workers.State.SetWorkers(q)
		m.Workers.Notes.SetWorkers(q)
		m.Verification.SetWorkers(q)
		if m.Workers.Rejections != nil {
			m.Workers.Rejections.SetSources(m.Coordinator.Context.Sessions, q)
		}
		m.Workers.Cards.SetQueue(q)
		m.Workers.Summaries.SetTasks(q)
		m.Workers.Results.SetWorkers(q)
		m.Workers.Workspaces.SetTasks(q)
		m.Coordinator.Guidance.SetWorkers(q)
		m.Coordinator.ProgressClosure.SetTasks(q)
		m.Coordinator.Loading.SetWorkers(q)
		m.Runner.Transcript.SetWorkers(q)
	}
}
