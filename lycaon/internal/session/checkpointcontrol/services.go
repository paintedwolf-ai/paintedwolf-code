package checkpointcontrol

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/session/promptstate"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcerewind"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	ExistingSessionIDs(ctx context.Context, ids []string) (map[string]bool, error)
	Get(ctx context.Context, id string) (*api.Session, error)
}

type Transcript interface {
	GetMessages(ctx context.Context, id string) ([]api.Message, error)
	LastTurnMessageContent(ctx context.Context, sessionID string) (string, error)
}

type Rewind interface {
	CommitRewind(ctx context.Context, id, sessionID, anchorMessageID string, response api.RewindSessionResponse) (int, error)
	CommittedRewindOperationsForSweep(ctx context.Context) ([]store.RewindOperationSweepItem, error)
	DeleteRewindOperation(ctx context.Context, id string, olderThan time.Time) error
	DropCheckpoint(ctx context.Context, session, anchor string) error
	DropRootCheckpoints(ctx context.Context, projectID, root string) error
	GetRewindOperation(ctx context.Context, id string) (*store.RewindOperation, error)
	PrepareRewind(ctx context.Context, op store.RewindOperation) error
	PutCheckpoint(ctx context.Context, root, session, anchor, sealed, manifest string, objects []store.CheckpointObject) error
	ReadCheckpoint(ctx context.Context, root, session, anchor string) (string, error)
	ReadCheckpointForRewind(ctx context.Context, session, anchor string) (string, error)
	RewindOperationsForRecovery(ctx context.Context) ([]store.RewindOperation, error)
	RewindOperationsForRecoverySession(ctx context.Context, sessionID string) ([]store.RewindOperation, error)
	SetRewindPhase(ctx context.Context, id, status, detail string) error
}

// Capture binds anchor intervals to each session's active workspace.
type Capture struct {
	dataDir      string
	sessionStore Sessions
	rewindStore  Rewind
	workspace    *sessionscope.Service
	Capture      checkpoint.Capture
}

func NewCapture(dataDir string, sessionStore Sessions, rewindStore Rewind, workspace *sessionscope.Service) *Capture {
	return &Capture{dataDir: dataDir, sessionStore: sessionStore, rewindStore: rewindStore, workspace: workspace}
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
	sessionStore    Sessions
	transcriptStore Transcript
	rewindStore     Rewind
	captures        *Capture
	prompt          *promptstate.MutexRegistry
	workspace       *sessionscope.Service
	projects        project.Registry
	sourceRewinds   *sourcerewind.Service
	events          *events.Publisher
	runtime         Runtime
}

func NewRewinds(sessionStore Sessions, transcriptStore Transcript, rewindStore Rewind, captures *Capture, prompt *promptstate.MutexRegistry, workspace *sessionscope.Service, projects project.Registry, sources *sourcerewind.Service, events *events.Publisher, runtime Runtime) *Rewinds {
	return &Rewinds{sessionStore: sessionStore, transcriptStore: transcriptStore, rewindStore: rewindStore, captures: captures, prompt: prompt, workspace: workspace, projects: projects, sourceRewinds: sources, events: events, runtime: runtime}
}

func (m *Capture) SetDataDir(root string) { m.dataDir = root }

func (m *Rewinds) SetProjects(registry project.Registry)    { m.projects = registry }
func (m *Rewinds) SetPublisher(publisher *events.Publisher) { m.events = publisher }
