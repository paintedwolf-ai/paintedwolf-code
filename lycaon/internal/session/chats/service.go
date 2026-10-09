package chats

import (
	"context"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/queue"
	"github.com/lycaon/lycaon/internal/resourcelifecycle"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/session/checkpointcontrol"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/promptstate"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

type Store interface {
	List(context.Context) ([]*api.Session, error)
	Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
	Get(context.Context, string) (*api.Session, error)
	ListProjectSummaries(context.Context, store.SummaryQuery) (store.SummaryPage, error)
	HasActiveProjectSessions(context.Context, string, time.Time) (bool, error)
	UpdateSession(context.Context, string, func(*api.Session)) error
	PinSession(context.Context, string) error
	UnpinSession(context.Context, string) error
	MovePinnedSession(context.Context, string, int) error
	GetWorktreeBinding(context.Context, string) (*store.WorktreeBinding, bool, error)
	SessionTreeIDs(context.Context, string) ([]string, error)
	Delete(context.Context, string) error
	MutationEventsOutboxed() bool
}
type Policy interface {
	Warm(context.Context, *api.Session)
}
type Workers interface {
	AbortAllWorkers(context.Context, string, string, string) error
}
type Loopback interface{ ForgetSession(string) }

// Service owns chat membership, ordering, and deletion of session trees.
type Service struct {
	store        Store
	policy       Policy
	prompt       *promptstate.MutexRegistry
	captures     *checkpointcontrol.Capture
	gate         *lifecycle.State
	queue        *queue.Store
	resources    *resourcelifecycle.Registry
	workers      Workers
	scratch      *scratch.Folders
	loopbackProv Loopback
	events       *events.Publisher
}

func New(store Store, policy Policy, prompt *promptstate.MutexRegistry, captures *checkpointcontrol.Capture, gate *lifecycle.State, queue *queue.Store, resources *resourcelifecycle.Registry) *Service {
	return &Service{store: store, policy: policy, prompt: prompt, captures: captures, gate: gate, queue: queue, resources: resources}
}
func (m *Service) SetWorkers(workers Workers)            { m.workers = workers }
func (m *Service) SetScratch(scratch *scratch.Folders)   { m.scratch = scratch }
func (m *Service) SetLoopback(loopback Loopback)         { m.loopbackProv = loopback }
func (m *Service) SetPublisher(events *events.Publisher) { m.events = events }
func (m *Service) removeScratch(ctx context.Context, ids []string) {
	if m.scratch == nil || len(ids) == 0 {
		return
	}
	if err := m.scratch.Remove(ids...); err != nil {
		slog.WarnContext(ctx, "remove deleted session scratch", "session_ids", ids, "error", err)
	}
}
