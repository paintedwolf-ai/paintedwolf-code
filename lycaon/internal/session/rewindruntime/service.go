// Package rewindruntime invalidates volatile state after a durable rewind commits.
package rewindruntime

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/queue"
	"github.com/lycaon/lycaon/pkg/api"
)

type Workers interface {
	ListBySession(context.Context, string, string, ...api.WorkerStatus) ([]api.WorkerTask, error)
}
type Touches interface{ ClearJob(string) }
type Forgetter interface{ Forget(string) }
type Settlement interface{ ReconcileSandbox(context.Context, string) }
type Batch interface {
	Apply(context.Context, string, batch.Event, int)
	BeginTurn(string)
}
type Closeouts interface{ Rewind(string, string) }
type History interface{ ForgetCalibration(string) }
type Drafts interface {
	Publish(context.Context, string, uint64)
}
type Submissions interface{ Publish(context.Context, string) }

type Service struct {
	Workers         Workers
	Touches         Touches
	Promotion       Forgetter
	Settlement      Settlement
	Batch           Batch
	Coordinator     *coordinator.Runtime
	Closeouts       Closeouts
	ProgressClosure Forgetter
	History         History
	Progress        progress.RunScopedStore
	Queue           *queue.Store
	Drafts          Drafts
	Submissions     Submissions
}

func (s *Service) ResetWorkers(ctx context.Context, session *api.Session, rootID string) {
	if s.Workers != nil && s.Touches != nil {
		jobs, err := s.Workers.ListBySession(ctx, session.ProjectID, rootID)
		if err == nil {
			for _, job := range jobs {
				s.Touches.ClearJob(job.ID)
			}
		}
	}
	s.Promotion.Forget(rootID)
	s.Settlement.ReconcileSandbox(ctx, rootID)
}

// ResetCoordinator fences kicks from the discarded boundary before waking the loop.
func (s *Service) ResetCoordinator(ctx context.Context, id string) {
	s.Batch.Apply(ctx, id, batch.EventVisibleUserMessage, 0)
	s.Batch.BeginTurn(id)
	s.Coordinator.Kicks().ClearPending(id)
	s.Coordinator.CoordinatorLoop().ClearPending(id)
	s.Coordinator.CoordinatorLoop().InterruptSleep(ctx, id)
}

func (s *Service) ResetTurnLedgers(sessionID, rootID string) {
	s.Closeouts.Rewind(sessionID, rootID)
	for _, id := range []string{sessionID, rootID} {
		s.ProgressClosure.Forget(id)
		s.History.ForgetCalibration(id)
	}
}

// Progress checklists have no version to restore.
func (s *Service) ResetProgress(rootID string) {
	if s.Progress != nil {
		_ = s.Progress.Set(rootID, "")
	}
}

// ResetQueue reconciles the draft with committed receipt cancellation.
func (s *Service) ResetQueue(ctx context.Context, id string) {
	if s.Queue == nil {
		return
	}
	s.Queue.Clear(id)
	s.Drafts.Publish(ctx, id, s.Queue.Snapshot(id).Revision)
	s.Submissions.Publish(context.WithoutCancel(ctx), id)
}
