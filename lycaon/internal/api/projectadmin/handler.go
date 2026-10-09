package projectadmin

import (
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/api/editoradmin"
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
	Projects     *Projects
	Promotion    *Promotion
	Removal      *Removal
	Roots        *Roots
	Sandboxes    *Sandboxes
	Secrets      *Secrets
	Trust        *Trust
	Verification *Verification
}

type Projects struct {
	Events          events.ReplayHub
	Extensions      *extensionadmin.Mutations
	MutationGate    *project.MutationGate
	Registry        project.Registry
	Roots           *Roots
	Sessions        *session.Host
	Settings        *settings.Service
	Store           session.Store
	Verification    *Verification
	responses       *httpio.Responder
	sourceWorkspace *sourceapi.Workspace
}

type Promotion struct {
	Git          *gitadmin.Handler
	MutationGate *project.MutationGate
	Projects     *Projects
	Registry     project.Registry
	Roots        *Roots
	Sessions     *session.Host
	Verification *Verification
	responses    *httpio.Responder
	sourceViews  *sourceapi.Views
}

type Removal struct {
	DataDir        string
	Database       db.Handle
	Extensions     *extensionadmin.Mutations
	ManagedSecrets *secretcap.Service
	MutationGate   *project.MutationGate
	Projects       *Projects
	Registry       project.Registry
	Owner          *projectremoval.Owner
	Roots          *Roots
	Sessions       *session.Host
	Settings       *settings.Service
	WorkerSeedRoot string
	responses      *httpio.Responder
	sourceViews    *sourceapi.Views
	sourceWatch    *sourceapi.Watch
}

type Roots struct {
	DataDir         string
	Git             *gitadmin.Handler
	MutationGate    *project.MutationGate
	ProjectRules    *rules.ProjectRulesOverlay
	Projects        *Projects
	Registry        project.Registry
	Sandboxes       *Sandboxes
	ScanCadence     *scancadence.Service
	Sessions        *session.Host
	Settings        *settings.Service
	Store           session.Store
	Verification    *Verification
	WorkerSeedRoot  string
	background      *taskgroup.Group
	responses       *httpio.Responder
	sourceEditor    *editoradmin.Handler
	sourceViews     *sourceapi.Views
	sourceWatch     *sourceapi.Watch
	sourceWorkspace *sourceapi.Workspace
}

type Sandboxes struct {
	Board                   *board.SnapshotBuilder
	Sessions                *session.Host
	WorkerBranchRoot        string
	Workers                 worker.WorkerQueue
	background              *taskgroup.Group
	sandboxReconcileLast    map[string]time.Time
	sandboxReconcileMu      sync.Mutex
	sandboxReconcilePending map[string]struct{}
}

type Secrets struct {
	ManagedSecrets *secretcap.Service
	MutationGate   *project.MutationGate
	Registry       project.Registry
	SecretIgnores  *projectignore.SecretService
	responses      *httpio.Responder
}

type Trust struct {
	Events     events.ReplayHub
	Extensions *extensionadmin.Mutations
	Registry   project.Registry
	Settings   *settings.Service
	responses  *httpio.Responder
}

type Verification struct {
	Events               events.ReplayHub
	LLMService           *llm.Service
	Registry             project.Registry
	Sessions             *session.Host
	Settings             *settings.Service
	background           *taskgroup.Group
	verifyDetectInFlight sync.Map
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
	h := Handler{}
	h.Projects = &Projects{Events: deps.Events, Extensions: deps.Extensions.Mutations, MutationGate: deps.MutationGate, Registry: deps.Registry, Sessions: deps.Sessions, Settings: deps.Settings, Store: deps.Store, responses: responses, sourceWorkspace: deps.Sources.Workspace}
	h.Promotion = &Promotion{Git: deps.Git, MutationGate: deps.MutationGate, Registry: deps.Registry, Sessions: deps.Sessions, responses: responses, sourceViews: deps.Sources.Views}
	h.Removal = &Removal{DataDir: deps.DataDir, Database: deps.Database, Extensions: deps.Extensions.Mutations, ManagedSecrets: deps.ManagedSecrets, MutationGate: deps.MutationGate, Registry: deps.Registry, Sessions: deps.Sessions, Settings: deps.Settings, WorkerSeedRoot: deps.WorkerSeedRoot, responses: responses, sourceViews: deps.Sources.Views, sourceWatch: deps.Sources.Watch}
	h.Roots = &Roots{DataDir: deps.DataDir, Git: deps.Git, MutationGate: deps.MutationGate, ProjectRules: deps.ProjectRules, Registry: deps.Registry, ScanCadence: deps.ScanCadence, Sessions: deps.Sessions, Settings: deps.Settings, Store: deps.Store, WorkerSeedRoot: deps.WorkerSeedRoot, background: background, responses: responses, sourceEditor: deps.Sources.Editor, sourceViews: deps.Sources.Views, sourceWatch: deps.Sources.Watch, sourceWorkspace: deps.Sources.Workspace}
	h.Sandboxes = &Sandboxes{Board: deps.Board, Sessions: deps.Sessions, WorkerBranchRoot: deps.WorkerBranchRoot, Workers: deps.Workers, background: background}
	h.Secrets = &Secrets{ManagedSecrets: deps.ManagedSecrets, MutationGate: deps.MutationGate, Registry: deps.Registry, SecretIgnores: deps.SecretIgnores, responses: responses}
	h.Trust = &Trust{Events: deps.Events, Extensions: deps.Extensions.Mutations, Registry: deps.Registry, Settings: deps.Settings, responses: responses}
	h.Verification = &Verification{Events: deps.Events, LLMService: deps.LLMService, Registry: deps.Registry, Sessions: deps.Sessions, Settings: deps.Settings, background: background}
	h.Projects.Roots = h.Roots
	h.Projects.Verification = h.Verification
	h.Promotion.Projects = h.Projects
	h.Promotion.Roots = h.Roots
	h.Promotion.Verification = h.Verification
	h.Removal.Projects = h.Projects
	h.Removal.Roots = h.Roots
	h.Roots.Projects = h.Projects
	h.Roots.Sandboxes = h.Sandboxes
	h.Roots.Verification = h.Verification
	return h
}
