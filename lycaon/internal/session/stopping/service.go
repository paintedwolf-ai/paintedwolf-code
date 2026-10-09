package stopping

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/queue"
	"github.com/lycaon/lycaon/internal/session/chats"
	"github.com/lycaon/lycaon/internal/session/draftqueue"
	"github.com/lycaon/lycaon/internal/session/execution"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

type Store interface {
	Get(context.Context, string) (*api.Session, error)
	GetPromptSubmission(context.Context, string) (*store.PromptSubmission, error)
	SessionTreeMembers(context.Context, string) ([]store.SessionTreeMember, error)
	InterruptRunningPromptSubmissionsBySession(context.Context, string) error
	InterruptPromptSubmissionsBySession(context.Context, string) error
	SetSessionStatus(context.Context, string, api.SessionStatus) error
}

// Service drains stopped runtimes before recording workflow and idle boundaries.
type Service struct {
	store              Store
	gate               *lifecycle.State
	execution          *execution.Lifetime
	chats              *chats.Service
	queue              *queue.Store
	drafts             *draftqueue.Service
	interruptions      *execution.Recovery
	status             *execution.Status
	runtime            *coordinator.Runtime
	Workers            WorkerAbort
	workflowStop       WorkflowStop
	checkpointStop     CheckpointStop
	progress           progress.RunScopedStore
	turnReleaseTimeout time.Duration
}

func New(store Store, gate *lifecycle.State, execution *execution.Lifetime, chats *chats.Service, queue *queue.Store, drafts *draftqueue.Service, interruptions *execution.Recovery, status *execution.Status) *Service {
	return &Service{store: store, gate: gate, execution: execution, chats: chats, queue: queue, drafts: drafts, interruptions: interruptions, status: status, turnReleaseTimeout: DefaultTurnReleaseTimeout}
}
func (m *Service) SetWorkers(workers WorkerAbort)               { m.Workers = workers }
func (m *Service) SetCoordinator(runtime *coordinator.Runtime)  { m.runtime = runtime }
func (m *Service) SetProgress(progress progress.RunScopedStore) { m.progress = progress }
func (m *Service) SetTurnReleaseTimeout(timeout time.Duration)  { m.turnReleaseTimeout = timeout }
func (m *Service) QueueChecklistReconcileNudge(ctx context.Context, id string) {
	if m == nil || m.progress == nil || m.runtime == nil {
		return
	}
	content := m.progress.Get(ctx, id)
	if !progress.HasOpenSteps(content) {
		return
	}
	_, pending, _ := progress.CloseCounts(content)
	m.runtime.Anchors().Emit(ctx, id, anchor.ProgressStale, anchor.Envelope{Vars: map[string]any{"pending": pending}})
}
