package extensionadmin

import (
	"sync"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/promptadmin"
	"github.com/lycaon/lycaon/internal/api/scanadmin"
	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/api/workflowadmin"
	"github.com/lycaon/lycaon/internal/contribframe"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/settings"
)

// Deps are the extension routes' dependencies, fixed at construction.
type Deps struct {
	// Owner serializes extension mutations.
	Owner       *extensionstate.Owner
	Runtime     ContributionRuntime
	Events      events.ReplayHub
	MCPRegistry *mcp.RegistryImpl
	ModuleRoot  string
	Projects    project.Registry
	Store       session.Store
	Sessions    *session.Manager
	Settings    *settings.Service
	Workflow    *workflowadmin.Handler
	Prompt      *promptadmin.Handler
	Scan        *scanadmin.Handler
}

type Handler struct {
	Deps
	extensionsOverview extensionsOverviewCache
	contribDeviceFrame *contribframe.Frame
	contribFrameOrder  []string
	contribFrames      map[string]*contribframe.Frame
	contribFramesMu    sync.Mutex
	background         *taskgroup.Group
	responses          *httpio.Responder
}

func New(responses *httpio.Responder, background *taskgroup.Group, deps Deps) Handler {
	httpio.RequireDependencies("extensionadmin",
		httpio.Required{Name: "MCPRegistry", Present: deps.MCPRegistry != nil},
		httpio.Required{Name: "Owner.Views", Present: deps.Owner != nil && deps.Owner.Views != nil},
		httpio.Required{Name: "Projects", Present: deps.Projects != nil},
		httpio.Required{Name: "Runtime.Authority", Present: deps.Runtime.Authority != nil},
		httpio.Required{Name: "Runtime.Receipts", Present: deps.Runtime.Receipts != nil},
		httpio.Required{Name: "Sessions", Present: deps.Sessions != nil},
		httpio.Required{Name: "Settings.TrustSurfaces", Present: deps.Settings != nil && deps.Settings.TrustSurfaces != nil},
		httpio.Required{Name: "Store", Present: deps.Store != nil},
	)
	return Handler{Deps: deps, responses: responses, background: background}
}
