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

type Sessions interface {
	Get(ctx context.Context, id string) (*api.Session, error)
	SessionTreeIDs(ctx context.Context, rootSessionID string) ([]string, error)
}

type Transcript interface {
	AppendMessages(ctx context.Context, id string, msgs ...api.Message) error
	GetMessage(ctx context.Context, sessionID, messageID string) (api.Message, error)
	GetMessages(ctx context.Context, id string) ([]api.Message, error)
	GetTranscriptPage(ctx context.Context, id string, q api.TranscriptPageQuery) (api.SessionTranscriptPage, error)
	GetWorkerJobMessages(ctx context.Context, sessionID, workerJobID string) ([]api.Message, error)
	MaxMessageScreenGenerationInTree(ctx context.Context, rootSessionID string) (uint64, error)
	MessageIDsBelowScreenGeneration(ctx context.Context, sessionID string, generation uint64) ([]string, error)
	MessageEventsOutboxed() bool
	PatchLiveProjection(ctx context.Context, sessionID, messageID, content string, toolCalls []api.ToolCall) error
	PatchMessageNavigation(ctx context.Context, sessionID, messageID, contentHash string, expected, refs []api.NavigationReference) (api.Message, error)
	StampMessageScreenGeneration(ctx context.Context, sessionID, messageID string, generation uint64) error
	ToolResultMessageID(ctx context.Context, sessionID, toolCallID string) (string, error)
	UpdateMessage(ctx context.Context, sessionID, messageID string, msg api.Message) (api.Message, error)
}

type Turns interface {
	ListPendingModelOutputProjections(ctx context.Context) ([]store.PendingModelOutputProjection, error)
	ListTurnLoadReceiptsForTurns(ctx context.Context, openingMessageIDs []string) ([]store.TurnLoadReceipt, error)
	MarkModelOutputProjected(ctx context.Context, outputID string) error
}

type People interface {
	HostOwner(context.Context) (people.Person, error)
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
type WorkflowDomains struct {
	Feedback   WorkflowFeedback
	Transcript WorkflowTranscript
}
type WorkflowFeedback interface {
	TryResolveUserFeedback(ctx context.Context, sessionID, messageID, authorPersonID, message string) error
}
type WorkflowTranscript interface {
	StampAndAppendMessages(ctx context.Context, sessionID string, msgs ...api.Message) error
}

type Reconciler interface {
	ReconcileOrphanedRuns(context.Context, string) error
}
type Redactor func(context.Context, api.Message) (api.Message, bool)

// Service owns durable transcript writes and their screened projections.
type Service struct {
	sessionStore            Sessions
	transcriptStore         Transcript
	turnStore               Turns
	people                  People
	projects                Projects
	Workspace               Workspace
	workerQueue             Workers
	workflows               *WorkflowDomains
	events                  *events.Publisher
	Streams                 *stream.State
	redactMessageForStorage Redactor
	secretSweeps            sweepQueue
	reconcile               Reconciler
	programEdits            programEdits
}

func New(sessionStore Sessions, transcriptStore Transcript, turnStore Turns, people People, workspace Workspace) *Service {
	return &Service{sessionStore: sessionStore, transcriptStore: transcriptStore, turnStore: turnStore, people: people, Workspace: workspace, Streams: stream.New(transcriptStore)}
}
func (m *Service) SetProjects(projects Projects)            { m.projects = projects }
func (m *Service) SetWorkers(workers Workers)               { m.workerQueue = workers }
func (m *Service) SetWorkflow(workflow *WorkflowDomains)    { m.workflows = workflow }
func (m *Service) SetPublisher(publisher *events.Publisher) { m.events = publisher }
func (m *Service) SetRedactor(redactor Redactor)            { m.redactMessageForStorage = redactor }
func (m *Service) Redact(ctx context.Context, message api.Message) (api.Message, bool) {
	if m == nil || m.redactMessageForStorage == nil {
		return message, false
	}
	return m.redactMessageForStorage(ctx, message)
}

func (m *Service) GetWorkerJobMessages(ctx context.Context, sessionID, jobID string) ([]api.Message, error) {
	messages, err := m.transcriptStore.GetWorkerJobMessages(ctx, sessionID, jobID)
	if err != nil {
		return nil, err
	}
	return m.Streams.StampMessages(sessionID, messages), nil
}

func (m *Service) SetPageReconciler(reconcile Reconciler) { m.reconcile = reconcile }
