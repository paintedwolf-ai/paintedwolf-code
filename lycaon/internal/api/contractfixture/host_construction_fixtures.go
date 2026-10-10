package contractfixture

import (
	"context"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/people/personactions"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testtool"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/apitestdeps"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/usernotice"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func NewServerForTest(t *testing.T, deps hostapi.Dependencies, opts ...TestDeps) *hostapi.Server {
	t.Helper()
	deps.Core.UserNotices = TestUserNotices(t)
	for _, opt := range opts {
		opt(&deps)
	}
	srv := hostapi.NewServer(RequiredTestDeps(t, deps), nil, hostapi.TestAPIToken)
	StopBackgroundOnCleanup(t, srv)
	return srv
}

// withSQLProjects keeps projects in the database the filled source ledger
// writes to, so the files a read tracks can reference their project.

func NewTestServer(t *testing.T, opts ...TestDeps) *hostapi.Server {
	t.Helper()
	return NewTestServerWithRegistry(t, tools.NewStubRegistry(), opts...)
}

func NewTestServerWithRegistry(t *testing.T, reg tools.ToolRegistry, opts ...TestDeps) *hostapi.Server {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	store := store.NewMemory()
	mock := llm.NewMockProvider(TestMockConfig(t))
	mgr := session.NewHost(store, session.Models{Client: mock, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, reg)
	mgr.SetDataDir(t.TempDir())
	mgr.Coordinator.Guards.SetToolMetadata(testtool.RegistryInvoker{Registry: reg})
	// Stub bindings do not expose coordinator tools.
	WireTestBindingRegistry(t)
	return NewServerForTest(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: store, Projects: project.NewMemoryRegistry(), Sessions: mgr}}, opts...)
}

// Ambient attach requires sessions and workflows in the same database.

func NewTestServerWithWorkflowRegistry(t *testing.T, reg tools.ToolRegistry, opts ...TestDeps) *hostapi.Server {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "store.db")

	sessions := store.NewSQL(sqlDB)
	mock := llm.NewMockProvider(TestMockConfig(t))
	mgr := session.NewHost(sessions, session.Models{Client: mock, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, reg)
	mgr.SetDataDir(t.TempDir())
	mgr.Coordinator.Guards.SetToolMetadata(testtool.RegistryInvoker{Registry: reg})
	WireTestBindingRegistry(t)

	registry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "workflowdef.RegistryFromDirs", err)
	runs := workflowpersistence.New(sqlDB)
	workflows := workflow.NewManager(runs, sessions, registry, nil)
	mgr.SetWorkflowDomains(&session.WorkflowDomains{Runs: workflows.Store.Runs, Policy: workflows.Policy, Ambient: workflows.Ambient, Blueprints: workflows.Blueprints, Batch: workflows.Batch, Slash: workflows.Slash, Requests: workflows.Requests, Feedback: workflows.Feedback, Transcript: workflows.Transcript, Asks: workflows.Asks, Fanout: workflows.Fanout, Phases: workflows.Phases, Reports: workflows.Reports, Recovery: workflows.Recovery, Cleanup: workflows})
	return NewServerForTest(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: sessions, PersonActions: personactions.New(sqlDB), Projects: project.NewSQLRegistry(sqlDB), Sessions: mgr}, Workflow: hostapi.WorkflowDependencies{
		Workflows: workflows, WorkflowRuns: runs}}, opts...)
}

// newServerForTest builds a server with the bundled user notices after opts
// adjust deps, and stops its background work when the test ends.

func NewTestServerWithWorkflows(t *testing.T, opts ...TestDeps) *hostapi.Server {
	t.Helper()
	return NewTestServerWithWorkflowRegistry(t, tools.NewStubRegistry(), opts...)
}

func RequiredTestDeps(t *testing.T, deps hostapi.Dependencies) hostapi.Dependencies {
	t.Helper()
	fill := apitestdeps.Deps{
		ApprovalGate:      deps.Approvals.ApprovalGate,
		ApprovalDecisions: deps.Approvals.Authority.ApprovalDecisions,
		Database:          deps.Core.Database, Store: deps.Core.Store, Projects: deps.Core.Projects, Sessions: deps.Core.Sessions,
		Settings: deps.Core.Settings, Invocations: deps.Core.Invocations, MutationGate: deps.Core.MutationGate,
		ManagedSecrets: deps.Approvals.ManagedSecrets, SecretIgnores: deps.Approvals.SecretIgnores, SourceLedger: deps.Source.SourceLedger,
		SourceMutations: deps.Source.SourceMutations, FileOperations: deps.Source.FileOperations,
		EditorDocuments: deps.Source.EditorDocuments, FileBriefings: deps.Source.FileBriefings, Workflows: deps.Workflow.Workflows,
		WorkflowRuns: deps.Workflow.WorkflowRuns, WorkflowComposer: deps.Workflow.WorkflowComposer, WorkflowPersister: deps.Workflow.WorkflowPersister,
		Blueprints: deps.Workflow.Blueprints, ScanCoordinator: deps.Scans.ScanCoordinator, ScanCadence: deps.Scans.ScanCadence,
		PublishDetections: deps.Scans.PublishDetections, DataDir: deps.Storage.DataDir, ModuleRoot: deps.Storage.ModuleRoot,
		MCP: deps.External.MCP, ExtensionViews: deps.Extensions.ExtensionViews,
		ContributionReceipts: deps.Extensions.Contributions.Receipts, ContributionAuthority: deps.Extensions.Contributions.Authority,
		LLM: deps.Providers.LLM, CostTracker: deps.Providers.CostTracker, Events: deps.Host.Events, EventPublisher: deps.Host.EventPublisher,
		HostIdentity: deps.Host.HostIdentity, Checkpoints: deps.Approvals.Checkpoints, ProgressStore: deps.Source.ProgressStore,
		VisualStore: deps.Source.VisualStore, HistoryStorage: deps.External.HistoryStorage, HostResources: deps.Host.HostResources,
		HostPower: deps.Host.HostPower, Pricing: deps.Host.Pricing, AgentPresence: deps.Source.AgentPresence, Workers: deps.Workflow.Workers,
		WorkerCancel: deps.Workflow.WorkerCancel, Delegations: deps.Workflow.Delegations, Board: deps.Workflow.Board,
		HarnessWorkers: deps.Harness.HarnessWorkers, WebResearch: deps.External.WebResearch, WebDiscoverer: deps.External.WebDiscoverer,
	}
	apitestdeps.Fill(t, &fill)
	deps.Approvals.ApprovalGate = fill.ApprovalGate
	deps.Approvals.Authority.ApprovalDecisions = fill.ApprovalDecisions
	deps.Core.Database, deps.Core.Store, deps.Core.Projects, deps.Core.Sessions = fill.Database, fill.Store, fill.Projects, fill.Sessions
	deps.Core.Settings, deps.Core.Invocations, deps.Core.MutationGate = fill.Settings, fill.Invocations, fill.MutationGate
	deps.Approvals.ManagedSecrets, deps.Approvals.SecretIgnores, deps.Source.SourceLedger = fill.ManagedSecrets, fill.SecretIgnores, fill.SourceLedger
	deps.Source.SourceMutations, deps.Source.FileOperations = fill.SourceMutations, fill.FileOperations
	if deps.Source.SourceInventory == nil {
		deps.Source.SourceInventory = fill.SourceLedger.Inventory
	}
	deps.Source.EditorDocuments, deps.Source.FileBriefings, deps.Workflow.Workflows = fill.EditorDocuments, fill.FileBriefings, fill.Workflows
	deps.Workflow.WorkflowRuns, deps.Workflow.WorkflowComposer, deps.Workflow.WorkflowPersister = fill.WorkflowRuns, fill.WorkflowComposer, fill.WorkflowPersister
	deps.Workflow.Blueprints, deps.Scans.ScanCoordinator, deps.Scans.ScanCadence = fill.Blueprints, fill.ScanCoordinator, fill.ScanCadence
	deps.Scans.PublishDetections, deps.Storage.DataDir, deps.Storage.ModuleRoot = fill.PublishDetections, fill.DataDir, fill.ModuleRoot
	deps.External.MCP, deps.Extensions.ExtensionViews = fill.MCP, fill.ExtensionViews
	deps.Extensions.Contributions.Receipts, deps.Extensions.Contributions.Authority = fill.ContributionReceipts, fill.ContributionAuthority
	deps.Providers.LLM, deps.Providers.CostTracker, deps.Host.Events, deps.Host.EventPublisher = fill.LLM, fill.CostTracker, fill.Events, fill.EventPublisher
	deps.Host.HostIdentity, deps.Approvals.Checkpoints, deps.Source.ProgressStore = fill.HostIdentity, fill.Checkpoints, fill.ProgressStore
	deps.Source.VisualStore, deps.External.HistoryStorage, deps.Host.HostResources = fill.VisualStore, fill.HistoryStorage, fill.HostResources
	deps.Host.HostPower, deps.Host.Pricing, deps.Source.AgentPresence = fill.HostPower, fill.Pricing, fill.AgentPresence
	deps.Workflow.Workers, deps.Workflow.WorkerCancel, deps.Workflow.Delegations, deps.Workflow.Board = fill.Workers, fill.WorkerCancel, fill.Delegations, fill.Board
	deps.Harness.HarnessWorkers = fill.HarnessWorkers
	deps.External.WebResearch, deps.External.WebDiscoverer = fill.WebResearch, fill.WebDiscoverer
	return deps
}

type TestDeps func(*hostapi.Dependencies)

func TestMockConfig(t *testing.T) *llm.MockConfig {
	t.Helper()
	cfg, err := llm.LoadMockConfig()
	if err != nil {
		t.Fatalf("load mock config: %v", err)
	}
	return cfg
}

func TestUserNotices(t *testing.T) *usernotice.Catalog {
	t.Helper()
	cfg, err := usernotice.LoadNoticeDir(filepath.Join(configlayout.FindModuleRoot(), "config", "packs", "painted-wolf", "platform", "host", "user-notices"))
	if err != nil {
		t.Fatalf("load user notices: %v", err)
	}
	return usernotice.NewCatalog(cfg)
}

func WithProjectMCP(t *testing.T, reg *mcp.Runtime) TestDeps {
	t.Helper()
	trust := WithTrustSurfaces(t)
	return func(d *hostapi.Dependencies) {
		trust(d)
		reg.Catalog.SetProjectOverlayGate(func(context.Context, string) bool { return true })
		d.External.MCP = reg
	}
}

func WithSQLProjects(t *testing.T) TestDeps {
	t.Helper()
	database := testdbfixture.Open(t, "projects.db")
	return func(d *hostapi.Dependencies) {
		d.Core.Database = database
		d.Core.Projects = project.NewSQLRegistry(database)
	}
}

// requiredTestDeps supplies every dependency the route families require that
// deps leaves unset.

func WithTrustSurfaces(t *testing.T) TestDeps {
	t.Helper()
	return func(d *hostapi.Dependencies) {
		if d.Core.Settings == nil {
			d.Core.Settings = &settings.Service{}
		}
		if d.Core.Settings.TrustSurfaces != nil {
			return
		}
		surfaces, err := settings.NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "trust-surfaces.yaml"))
		testutil.FailErr(t, "trust surfaces store", err)
		d.Core.Settings.TrustSurfaces = surfaces
	}
}

// withProjectMCP serves reg with its project-layer gate open.

func PrimaryRootPath(p wire.Project) string {
	for _, r := range p.Roots {
		if r.IsPrimary {
			return r.Path
		}
	}
	if len(p.Roots) > 0 {
		return p.Roots[0].Path
	}
	return ""
}

func ReadBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

// primaryRootPath mirrors the primary-or-first root selection Den applies to wire projects.

func ResolveTestPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("eval symlinks %q: %v", p, err)
	}
	return r
}
