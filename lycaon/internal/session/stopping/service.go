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

type Sessions interface {
	Get(ctx context.Context, id string) (*api.Session, error)
	SessionTreeMembers(ctx context.Context, rootID string) ([]store.SessionTreeMember, error)
	SetSessionStatus(ctx context.Context, id string, status api.SessionStatus) error
}

type Submissions interface {
	GetPromptSubmission(ctx context.Context, id string) (*store.PromptSubmission, error)
	InterruptPromptSubmissionsBySession(ctx context.Context, sessionID string) error
	InterruptRunningPromptSubmissionsBySession(ctx context.Context, sessionID string) error
}

// Service drains stopped runtimes before recording workflow and idle boundaries.
type Service struct {
	sessionStore         Sessions
	submissionStore      Submissions
	gate                 *lifecycle.State
	execution            *execution.Lifetime
	chats                *chats.Service
	queue                *queue.Store
	drafts               *draftqueue.Service
	Recovery             *execution.Recovery
	status               *execution.Status
	runtime              *coordinator.Runtime
	Workers              WorkerAbort
	workflowStop         WorkflowStop
	checkpointStop       CheckpointStop
	progress             progress.RunScopedStore
	turnReleaseTimeout   time.Duration
	workerReleaseTimeout time.Duration
}

func New(sessionStore Sessions, submissionStore Submissions, gate *lifecycle.State, execution *execution.Lifetime, chats *chats.Service, queue *queue.Store, drafts *draftqueue.Service, interruptions *execution.Recovery, status *execution.Status) *Service {
	return &Service{
		sessionStore: sessionStore, submissionStore: submissionStore,
		gate:                 gate,
		execution:            execution,
		chats:                chats,
		queue:                queue,
		drafts:               drafts,
		Recovery:             interruptions,
		status:               status,
		turnReleaseTimeout:   DefaultTurnReleaseTimeout,
		workerReleaseTimeout: DefaultWorkerReleaseTimeout,
	}
}
func (m *Service) SetWorkers(workers WorkerAbort)                { m.Workers = workers }
func (m *Service) SetCoordinator(runtime *coordinator.Runtime)   { m.runtime = runtime }
func (m *Service) SetProgress(progress progress.RunScopedStore)  { m.progress = progress }
func (m *Service) SetTurnReleaseTimeout(timeout time.Duration)   { m.turnReleaseTimeout = timeout }
func (m *Service) SetWorkerReleaseTimeout(timeout time.Duration) { m.workerReleaseTimeout = timeout }
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
