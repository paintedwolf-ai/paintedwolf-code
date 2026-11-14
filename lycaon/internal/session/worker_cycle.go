package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/session/projectcontrol"
	"github.com/lycaon/lycaon/internal/session/turnguards"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkerCycleLister loads worker jobs for coordinator cycle guards and outcome bridging.
type WorkerCycleLister interface {
	ListBySession(ctx context.Context, projectID, sessionID string, status ...api.WorkerStatus) ([]api.WorkerTask, error)
	ListPendingOutcomes(ctx context.Context) ([]api.WorkerTask, error)
	Get(jobID string) (*api.WorkerTask, bool)
	ClaimWorkerBranch(ctx context.Context, jobID string) (*api.WorkerTask, error)
	// ReviewsWorker reports a read job that reviews a sealed result.
	ReviewsWorker(ctx context.Context, task *api.WorkerTask) bool
}

// SetWorkerRounds sets the policy that bounds worker rounds and their notices.
func (m *Host) SetWorkerRounds(policy spawn.RoundPolicy) {
	if m != nil {
		m.Coordinator.Nudging.Rounds = policy
	}
}

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

// SetWorkerNames sets how a job is traced to the worker whose round it runs,
// for waits and coordinator notices that address workers by name.
func (m *Host) SetWorkerNames(names workercompletion.WorkerNames) {
	if m == nil {
		return
	}
	if m.Coordinator.Loop != nil {
		m.Coordinator.Loop.WorkerNames = names
	}
	if m.Workers.Results != nil {
		m.Workers.Results.SetWorkerNames(names)
	}
	if m.Workers.Summaries != nil {
		m.Workers.Summaries.SetWorkerNames(names)
	}
	if m.Workers.Cancellations != nil {
		m.Workers.Cancellations.SetWorkerNames(names)
	}
	if m.Workers.Cards != nil {
		m.Workers.Cards.SetWorkerNames(names)
	}
	if m.Coordinator.Workers != nil {
		m.Coordinator.Workers.Names = names
	}
	if m.ProjectControl != nil {
		m.ProjectControl.SetWorkerNames(names)
	}
	if m.Workers.Workspaces != nil {
		m.Workers.Workspaces.SetWorkerNames(names)
	}
}

// SetWorkerResultNotices sets how a settled round is described in its
// coordinator's notice.
func (m *Host) SetWorkerResultNotices(notices workeroutcomes.ResultNotices) {
	if m == nil || m.Workers.Results == nil {
		return
	}
	m.Workers.Results.SetResultNotices(notices)
}

// SetJobNames sets how closeout refusals name a pending job's worker and result.
func (m *Host) SetJobNames(names turnguards.JobNames) {
	if m != nil && m.Coordinator.Guards != nil {
		m.Coordinator.Guards.SetJobNames(names)
	}
}

// SetWorkerResults sets how a worker name resolves to its latest result for
// the checklist latch.
func (m *Host) SetWorkerResults(results turnguards.LatestResultJobs) {
	if m != nil && m.Coordinator.Guards != nil {
		m.Coordinator.Guards.SetWorkerResults(results)
	}
}

// SetDeliveredWorkers sets how surface selection counts delivered workers.
func (m *Host) SetDeliveredWorkers(count workeroutcomes.DeliveredWorkers) {
	if m != nil && m.Workers.State != nil {
		m.Workers.State.SetDeliveredWorkers(count)
	}
}

// SetWorkerRoster sets how a coordinator's request lists its workers.
func (m *Host) SetWorkerRoster(roster assembly.WorkerRosterSource) {
	if m == nil || m.Coordinator.Assembly == nil {
		return
	}
	m.Coordinator.Assembly.WorkerRoster = roster
}

// SetWorkerMessages sets how messages between a coordinator and its workers
// reach their requests.
func (m *Host) SetWorkerMessages(delivery assembly.MessageDeliveryRecorder) {
	if m == nil || m.Coordinator.Assembly == nil {
		return
	}
	m.Coordinator.Assembly.MessageDelivery = delivery
}
