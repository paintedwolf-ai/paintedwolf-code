package transcript

import (
	"context"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/stream"
	"github.com/lycaon/lycaon/pkg/api"
)

type Store interface {
	PatchMessageNavigation(context.Context, string, string, string, []api.NavigationReference, []api.NavigationReference) (api.Message, error)
	ListPendingModelOutputProjections(context.Context) ([]store.PendingModelOutputProjection, error)
	MarkModelOutputProjected(context.Context, string) error
	people.OwnerSource
	stream.ProjectionStore
	Get(context.Context, string) (*api.Session, error)
	AppendMessages(context.Context, string, ...api.Message) error
	GetMessage(context.Context, string, string) (api.Message, error)
	GetMessages(context.Context, string) ([]api.Message, error)
	GetTranscriptPage(context.Context, string, api.TranscriptPageQuery) (api.SessionTranscriptPage, error)
	ListTurnLoadReceiptsForTurns(context.Context, []string) ([]store.TurnLoadReceipt, error)
	GetWorkerJobMessages(context.Context, string, string) ([]api.Message, error)
	UpdateMessage(context.Context, string, string, api.Message) (api.Message, error)
	MutationEventsOutboxed() bool
	SessionTreeIDs(context.Context, string) ([]string, error)
	MaxMessageScreenGenerationInTree(context.Context, string) (uint64, error)
	MessageIDsBelowScreenGeneration(context.Context, string, uint64) ([]string, error)
	StampMessageScreenGeneration(context.Context, string, string, uint64) error
}

type Projects interface {
	Get(context.Context, string) (*project.Project, error)
}
type Workspace interface {
	Roots(context.Context, *api.Session) ([]projectroot.RootRef, error)
	Binding(context.Context, *api.Session) (project.Binding, bool, error)
}
type Workers interface {
	Get(string) (*api.WorkerTask, bool)
}
type Workflow interface {
	TryResolveUserFeedback(context.Context, string, string, string, string) error
	StampAndAppendMessages(context.Context, string, ...api.Message) error
}
type Reconciler interface {
	ReconcileOrphanedRuns(context.Context, string) error
}
type Redactor func(context.Context, api.Message) (api.Message, bool)

// Service owns durable transcript writes and their screened projections.
type Service struct {
	store                   Store
	projects                Projects
	Workspace               Workspace
	workerQueue             Workers
	workflows               Workflow
	events                  *events.Publisher
	Streams                 *stream.State
	redactMessageForStorage Redactor
	secretSweeps            sweepQueue
	reconcile               Reconciler
}

func New(store Store, workspace Workspace) *Service {
	return &Service{store: store, Workspace: workspace, Streams: stream.New(store)}
}
func (m *Service) SetProjects(projects Projects)            { m.projects = projects }
func (m *Service) SetWorkers(workers Workers)               { m.workerQueue = workers }
func (m *Service) SetWorkflow(workflow Workflow)            { m.workflows = workflow }
func (m *Service) SetPublisher(publisher *events.Publisher) { m.events = publisher }
func (m *Service) SetRedactor(redactor Redactor)            { m.redactMessageForStorage = redactor }
func (m *Service) Redact(ctx context.Context, message api.Message) (api.Message, bool) {
	if m == nil || m.redactMessageForStorage == nil {
		return message, false
	}
	return m.redactMessageForStorage(ctx, message)
}

func (m *Service) GetWorkerJobMessages(ctx context.Context, sessionID, jobID string) ([]api.Message, error) {
	messages, err := m.store.GetWorkerJobMessages(ctx, sessionID, jobID)
	if err != nil {
		return nil, err
	}
	return m.Streams.StampMessages(sessionID, messages), nil
}

func (m *Service) SetPageReconciler(reconcile Reconciler) { m.reconcile = reconcile }
