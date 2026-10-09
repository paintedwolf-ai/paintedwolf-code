package projectadmin

import (
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/api/extensionadmin"
	"github.com/lycaon/lycaon/internal/api/gitadmin"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectignore"
	"github.com/lycaon/lycaon/internal/projectremoval"
	"github.com/lycaon/lycaon/internal/rules"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/worker"
)

// Deps are the project routes' dependencies, fixed at construction.
type Deps struct {
	SecretIgnores *projectignore.SecretService
	Extensions    *extensionadmin.Handler
	Board         *board.SnapshotBuilder
	// Database holds the project removal journal.
	Database       db.Handle
	DataDir        string
	Events         events.ReplayHub
	LLMService     *llm.Service
	ManagedSecrets *secretcap.Service
	MutationGate   *project.MutationGate
	Registry       project.Registry
	ProjectRules   *rules.ProjectRulesOverlay
	ScanCadence    *scancadence.Service
	Store          session.Store
	Sessions       *session.Host
	Settings       *settings.Service
	// WorkerSeedRoot and WorkerBranchRoot hold host-only worker storage.
	WorkerSeedRoot   string
	WorkerBranchRoot string
	Workers          worker.WorkerQueue
	Sources          *sourceapi.Handler
	Git              *gitadmin.Handler
}

type Handler struct {
	Deps
	// Removal is set by InitProjectRemoval once the handler is at its final address.
	Removal                 *projectremoval.Owner
	verifyDetectInFlight    sync.Map
	sandboxReconcileMu      sync.Mutex
	sandboxReconcileLast    map[string]time.Time
	sandboxReconcilePending map[string]struct{}
	responses               *httpio.Responder
	background              *taskgroup.Group
}

func New(responses *httpio.Responder, background *taskgroup.Group, deps Deps) Handler {
	httpio.RequireDependencies("projectadmin",
		httpio.Required{Name: "Database", Present: deps.Database != nil},
		httpio.Required{Name: "LLMService", Present: deps.LLMService != nil},
		httpio.Required{Name: "ManagedSecrets", Present: deps.ManagedSecrets != nil},
		httpio.Required{Name: "MutationGate", Present: deps.MutationGate != nil},
		httpio.Required{Name: "Registry", Present: deps.Registry != nil},
		httpio.Required{Name: "SecretIgnores", Present: deps.SecretIgnores != nil && deps.SecretIgnores.Roots != nil},
		httpio.Required{Name: "Sessions", Present: deps.Sessions != nil},
		httpio.Required{Name: "Settings.TrustSurfaces", Present: deps.Settings != nil && deps.Settings.TrustSurfaces != nil},
		httpio.Required{Name: "Settings.Verify", Present: deps.Settings != nil && deps.Settings.Verify != nil},
		httpio.Required{Name: "Store", Present: deps.Store != nil},
	)
	return Handler{Deps: deps, responses: responses, background: background}
}
