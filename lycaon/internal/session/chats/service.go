package chats

import (
	"context"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/queue"
	"github.com/lycaon/lycaon/internal/resourcelifecycle"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/session/checkpointcontrol"
	"github.com/lycaon/lycaon/internal/session/draftqueue"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/naming"
	"github.com/lycaon/lycaon/internal/session/promptstate"
	"github.com/lycaon/lycaon/internal/session/protection"
	"github.com/lycaon/lycaon/internal/session/recovery"
	"github.com/lycaon/lycaon/internal/session/researchwarm"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	SessionEventsOutboxed() bool
	Create(ctx context.Context, req api.CreateSessionRequest, projectID string) (*api.Session, error)
	Delete(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (*api.Session, error)
	GetWorktreeBinding(ctx context.Context, sessionID string) (*store.WorktreeBinding, bool, error)
	HasActiveProjectSessions(ctx context.Context, projectID string, activeSince time.Time) (bool, error)
	List(ctx context.Context) ([]*api.Session, error)
	ListProjectSummaries(ctx context.Context, q store.SummaryQuery) (store.SummaryPage, error)
	MovePinnedSession(ctx context.Context, id string, position int) error
	PinSession(ctx context.Context, id string) error
	SessionTreeIDs(ctx context.Context, rootSessionID string) ([]string, error)
	UnpinSession(ctx context.Context, id string) error
	UpdateSession(ctx context.Context, id string, fn func(*api.Session)) error
}

type Policy interface {
	Warm(context.Context, *api.Session)
}
type Workers interface {
	AbortAllWorkers(context.Context, string, string, string) error
}

// Service owns chat membership, ordering, and deletion of session trees.
type Service struct {
	Gate       *lifecycle.State
	Captures   *checkpointcontrol.Capture
	Naming     *naming.Service
	Research   *researchwarm.Service
	Protection *protection.Service
	Recovery   *recovery.Service
	Rewinds    *checkpointcontrol.Rewinds
	Drafts     *draftqueue.Service

	sessionStore Sessions
	policy       Policy
	prompt       *promptstate.MutexRegistry
	queue        *queue.Store
	resources    *resourcelifecycle.Registry
	workers      Workers
	scratch      *scratch.Folders
	events       *events.Publisher
	processes    *bgprocess.Registry
}

func New(sessionStore Sessions, policy Policy, prompt *promptstate.MutexRegistry, queue *queue.Store, resources *resourcelifecycle.Registry, l Lifecycle) *Service {
	return &Service{sessionStore: sessionStore, policy: policy, prompt: prompt, queue: queue, resources: resources, Gate: l.Gate, Captures: l.Captures, Naming: l.Naming, Research: l.Research, Protection: l.Protection, Recovery: l.Recovery, Rewinds: l.Rewinds, Drafts: l.Drafts}
}

func (m *Service) SetWorkers(workers Workers)                 { m.workers = workers }
func (m *Service) SetScratch(scratch *scratch.Folders)        { m.scratch = scratch }
func (m *Service) SetPublisher(events *events.Publisher)      { m.events = events }
func (m *Service) SetProcesses(processes *bgprocess.Registry) { m.processes = processes }
func (m *Service) removeScratch(ctx context.Context, ids []string) {
	if m.scratch == nil || len(ids) == 0 {
		return
	}
	if err := m.scratch.Remove(ids...); err != nil {
		slog.WarnContext(ctx, "remove deleted session scratch", "session_ids", ids, "error", err)
	}
}

type Lifecycle struct {
	Gate       *lifecycle.State
	Captures   *checkpointcontrol.Capture
	Naming     *naming.Service
	Research   *researchwarm.Service
	Protection *protection.Service
	Recovery   *recovery.Service
	Rewinds    *checkpointcontrol.Rewinds
	Drafts     *draftqueue.Service
}
