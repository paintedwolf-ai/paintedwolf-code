package workflowadmin

import (
	"sync"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/api/sessionadmin"
	"github.com/lycaon/lycaon/internal/api/sessionview"
	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/session"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowinputs "github.com/lycaon/lycaon/internal/workflow/inputs"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
)

// Deps are the workflow routes' dependencies, fixed at construction.
type Deps struct {
	Workflows *workflow.RunManager
	// Catalog and Runs back workflow discovery and session run history.
	Catalog        workflowcatalog.Resolver
	Runs           *runstate.Repository
	Composer       *workflowcomposition.Composer
	Persister      *workflowcomposition.Persister
	Blueprints     *blueprint.Manager
	Orchestrator   orchestration.Orchestrator
	EventPublisher *events.Publisher
	ManagedSecrets *secretcap.Service
	Projects       project.Registry
	Scans          scan.ScanCoordinator
	Store          *sessionstore.Repositories
	Sessions       *session.Host
	VisualStore    visual.Store
	Workers        worker.WorkerQueue
	SessionAdmin   *sessionadmin.Handler
	SessionView    *sessionview.Projector
}

type Handler struct {
	BlueprintRoutes *BlueprintRoutes
	Composition     *Composition
	Reports         *Reports
	RunControl      *RunControl
	Topology        *Topology
}

type BlueprintRoutes struct {
	RunControl   *RunControl
	Blueprints   *blueprint.Manager
	Catalog      workflowcatalog.Resolver
	Projects     project.Registry
	Runs         *runstate.Repository
	SessionAdmin *sessionadmin.Lifecycle
	Topology     *Topology
	Workflows    *workflow.RunManager
	responses    *httpio.Responder
}

type Composition struct {
	Composer       *workflowcomposition.Composer
	EventPublisher *events.Publisher
	Persister      *workflowcomposition.Persister
	SessionView    *sessionview.Projector
	Sessions       *session.Host
	SessionStore   requestscope.Sessions
	responses      *httpio.Responder
}

type Reports struct {
	RunControl      *RunControl
	Projects        project.Registry
	Runs            *runstate.Repository
	Scans           scan.ScanCoordinator
	SessionStore    requestscope.Sessions
	TranscriptStore sessionstore.TranscriptRepository
	VisualStore     visual.Store
	Workers         worker.WorkerQueue
	Workflows       *workflow.RunManager
	responses       *httpio.Responder
}

type RunControl struct {
	Catalog        workflowcatalog.Resolver
	ManagedSecrets *secretcap.Service
	Projects       project.Registry
	Runs           *runstate.Repository
	SessionView    *sessionview.Projector
	Sessions       *session.Host
	SessionStore   requestscope.Sessions
	Topology       *Topology
	Workflows      *workflow.RunManager
	responses      *httpio.Responder
}

type Topology struct {
	Requests           *workflowinputs.Requests
	Orchestrator       orchestration.Orchestrator
	Runs               *runstate.Repository
	SessionStore       requestscope.Sessions
	activeTopologyRuns sync.Map // workflow run id → struct{} while topology settlement is executing
	background         *taskgroup.Group
}

func New(responses *httpio.Responder, background *taskgroup.Group, deps Deps) Handler {
	httpio.RequireDependencies("workflowadmin",
		httpio.Required{Name: "Blueprints", Present: deps.Blueprints != nil},
		httpio.Required{Name: "Composer", Present: deps.Composer != nil},
		httpio.Required{Name: "Persister", Present: deps.Persister != nil},
		httpio.Required{Name: "Projects", Present: deps.Projects != nil},
		httpio.Required{Name: "Runs", Present: deps.Runs != nil},
		httpio.Required{Name: "Sessions", Present: deps.Sessions != nil},
		httpio.Required{Name: "Store.Sessions", Present: deps.Store != nil && deps.Store.Sessions != nil},
		httpio.Required{Name: "Store.Transcript", Present: deps.Store != nil && deps.Store.Transcript != nil},
		httpio.Required{Name: "Workflows", Present: deps.Workflows != nil},
	)
	h := Handler{}
	h.BlueprintRoutes = &BlueprintRoutes{Runs: deps.Runs, Blueprints: deps.Blueprints, Catalog: deps.Catalog, Projects: deps.Projects, SessionAdmin: deps.SessionAdmin.Lifecycle, Workflows: deps.Workflows, responses: responses}
	h.Composition = &Composition{Composer: deps.Composer, EventPublisher: deps.EventPublisher, Persister: deps.Persister, SessionView: deps.SessionView, Sessions: deps.Sessions, SessionStore: deps.Store.Sessions, responses: responses}
	h.Reports = &Reports{Runs: deps.Runs, Projects: deps.Projects, Scans: deps.Scans, SessionStore: deps.Store.Sessions, TranscriptStore: deps.Store.Transcript, VisualStore: deps.VisualStore, Workers: deps.Workers, Workflows: deps.Workflows, responses: responses}
	h.RunControl = &RunControl{Runs: deps.Runs, Catalog: deps.Catalog, ManagedSecrets: deps.ManagedSecrets, Projects: deps.Projects, SessionView: deps.SessionView, Sessions: deps.Sessions, SessionStore: deps.Store.Sessions, Workflows: deps.Workflows, responses: responses}
	h.Topology = &Topology{Requests: deps.Workflows.Requests, Runs: deps.Runs, Orchestrator: deps.Orchestrator, SessionStore: deps.Store.Sessions, background: background}

	h.BlueprintRoutes.Topology = h.Topology

	h.RunControl.Topology = h.Topology

	h.BlueprintRoutes.RunControl = h.RunControl
	h.Reports.RunControl = h.RunControl
	return h
}
