package workflowadmin

import (
	"sync"

	"github.com/lycaon/lycaon/internal/api/httpio"
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
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
)

// Deps are the workflow routes' dependencies, fixed at construction.
type Deps struct {
	Workflows *workflow.RunManager
	// Catalog and Runs back workflow discovery and session run history.
	Catalog        workflow.ManifestResolver
	Runs           workflow.RunStore
	Composer       *workflow.Composer
	Persister      *workflow.Persister
	Blueprints     *blueprint.Manager
	Orchestrator   orchestration.Orchestrator
	EventPublisher *events.Publisher
	ManagedSecrets *secretcap.Service
	Projects       project.Registry
	Scans          scan.ScanCoordinator
	Store          session.Store
	Sessions       *session.Manager
	VisualStore    visual.Store
	Workers        worker.WorkerQueue
	SessionAdmin   *sessionadmin.Handler
	SessionView    *sessionview.Projector
}

type Handler struct {
	Deps
	activeTopologyRuns sync.Map // workflow run id → struct{} while topology settlement is executing
	responses          *httpio.Responder
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
		httpio.Required{Name: "Store", Present: deps.Store != nil},
		httpio.Required{Name: "Workflows", Present: deps.Workflows != nil},
	)
	return Handler{Deps: deps, responses: responses, background: background}
}
