package checkpointcontrol

import (
	"context"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/session/promptstate"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcerewind"
	"github.com/lycaon/lycaon/pkg/api"
	"time"
)

type CaptureRepository interface {
	store.CheckpointRepository
	Get(context.Context, string) (*api.Session, error)
	ExistingSessionIDs(context.Context, []string) (map[string]bool, error)
}

type RewindRepository interface {
	store.CheckpointRepository
	Get(context.Context, string) (*api.Session, error)
	GetMessages(context.Context, string) ([]api.Message, error)
	LastTurnMessageContent(context.Context, string) (string, error)
	GetRewindOperation(context.Context, string) (*store.RewindOperation, error)
	PrepareRewind(context.Context, store.RewindOperation) error
	SetRewindPhase(context.Context, string, string, string) error
	CommitRewind(context.Context, string, string, string, api.RewindSessionResponse) (int, error)
	RewindOperationsForRecovery(context.Context) ([]store.RewindOperation, error)
	RewindOperationsForRecoverySession(context.Context, string) ([]store.RewindOperation, error)
	CommittedRewindOperationsForSweep(context.Context) ([]store.RewindOperationSweepItem, error)
	DeleteRewindOperation(context.Context, string, time.Time) error
}

// Capture binds anchor intervals to each session's active workspace.
type Capture struct {
	dataDir   string
	store     CaptureRepository
	workspace *sessionscope.Service
	Capture   checkpoint.Capture
}

func NewCapture(dataDir string, repository CaptureRepository, workspace *sessionscope.Service) *Capture {
	return &Capture{dataDir: dataDir, store: repository, workspace: workspace}
}

// Runtime connects rewind settlement to the domains whose volatile state it invalidates.
type Runtime struct {
	WorkersInFlight  func(context.Context, *api.Session) int
	ResetWorkers     func(context.Context, *api.Session, string)
	ResetCoordinator func(context.Context, string)
	ResetTurnLedgers func(string, string)
	ResetProgress    func(string)
	ResetQueue       func(context.Context, string)
}

// Rewinds owns preview, atomic application, receipt replay, and boot recovery.
type Rewinds struct {
	store         RewindRepository
	captures      *Capture
	prompt        *promptstate.MutexRegistry
	workspace     *sessionscope.Service
	projects      project.Registry
	sourceRewinds *sourcerewind.Service
	events        *events.Publisher
	runtime       Runtime
}

func NewRewinds(repository RewindRepository, captures *Capture, prompt *promptstate.MutexRegistry, workspace *sessionscope.Service, projects project.Registry, sources *sourcerewind.Service, events *events.Publisher, runtime Runtime) *Rewinds {
	return &Rewinds{store: repository, captures: captures, prompt: prompt, workspace: workspace, projects: projects, sourceRewinds: sources, events: events, runtime: runtime}
}

func (m *Capture) SetDataDir(root string) { m.dataDir = root }

func (m *Rewinds) SetProjects(registry project.Registry)    { m.projects = registry }
func (m *Rewinds) SetPublisher(publisher *events.Publisher) { m.events = publisher }
