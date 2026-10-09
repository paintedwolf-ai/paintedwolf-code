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
	MCPRegistry *mcp.Runtime
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
	Catalog       *Catalog
	Contributions *Contributions
	Execution     *Execution
	Mutations     *Mutations
	Suggestions   *Suggestions
}

type Catalog struct {
	Mutations          *Mutations
	Projects           project.Registry
	extensionsOverview extensionsOverviewCache
	responses          *httpio.Responder
}

type Contributions struct {
	MCP                *mcp.ProviderCatalog
	MCPCalls           *mcp.ToolCalls
	Projects           project.Registry
	Sessions           *session.Manager
	contribDeviceFrame *contribframe.Frame
	contribFrameOrder  []string
	contribFrames      map[string]*contribframe.Frame
	contribFramesMu    sync.Mutex
	responses          *httpio.Responder
}

type Execution struct {
	Contributions    *Contributions
	MCP              *mcp.ToolCalls
	Projects         project.Registry
	PromptSubmission *promptadmin.Submission
	PromptReferences *promptadmin.References
	PromptExecution  *promptadmin.Execution
	Runtime          ContributionRuntime
	Sessions         *session.Manager
	Store            session.Store
	Workflow         *workflowadmin.RunControl
	responses        *httpio.Responder
}

type Mutations struct {
	Catalog       *Catalog
	Contributions *Contributions
	Events        events.ReplayHub
	MCP           *mcp.ConnectionPool
	ModuleRoot    string
	Owner         *extensionstate.Owner
	Projects      project.Registry
	Scan          *scanadmin.Handler
	Sessions      *session.Manager
	Settings      *settings.Service
	background    *taskgroup.Group
	responses     *httpio.Responder
}

type Suggestions struct {
	Catalog   *Catalog
	Mutations *Mutations
	Projects  project.Registry
	Settings  *settings.Service
	responses *httpio.Responder
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
	h := Handler{}
	h.Catalog = &Catalog{Projects: deps.Projects, responses: responses}
	h.Contributions = &Contributions{MCP: deps.MCPRegistry.Catalog, MCPCalls: deps.MCPRegistry.Calls, Projects: deps.Projects, Sessions: deps.Sessions, responses: responses}
	h.Execution = &Execution{MCP: deps.MCPRegistry.Calls, Projects: deps.Projects, PromptSubmission: deps.Prompt.Submission, PromptReferences: deps.Prompt.References, PromptExecution: deps.Prompt.Execution, Runtime: deps.Runtime, Sessions: deps.Sessions, Store: deps.Store, Workflow: deps.Workflow.RunControl, responses: responses}
	h.Mutations = &Mutations{Events: deps.Events, MCP: deps.MCPRegistry.Connections, ModuleRoot: deps.ModuleRoot, Owner: deps.Owner, Projects: deps.Projects, Scan: deps.Scan, Sessions: deps.Sessions, Settings: deps.Settings, background: background, responses: responses}
	h.Suggestions = &Suggestions{Projects: deps.Projects, Settings: deps.Settings, responses: responses}
	h.Catalog.Mutations = h.Mutations
	h.Execution.Contributions = h.Contributions
	h.Mutations.Catalog = h.Catalog
	h.Mutations.Contributions = h.Contributions
	h.Suggestions.Catalog = h.Catalog
	h.Suggestions.Mutations = h.Mutations
	deps.Owner.Publisher = extensionPublisher{h.Mutations}
	deps.Owner.Events = extensionEmitter{h.Mutations}
	return h
}
