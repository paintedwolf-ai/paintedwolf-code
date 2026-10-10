// Package apitestdeps builds the host services every API route family requires,
// for tests that construct an API server around a few services of their own.
package apitestdeps

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/commandinvoke"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/fileops"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/harnessfixture"
	"github.com/lycaon/lycaon/internal/historyretention"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostidentity"
	"github.com/lycaon/lycaon/internal/hostpower"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectignore"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/scan"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/session"
	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	repoinfotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"github.com/lycaon/lycaon/internal/testtool"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Deps mirrors the API dependencies a server cannot be built without.
type Deps struct {
	ApprovalDecisions interface {
		ListApprovalDecisionsSince(context.Context, time.Time) ([]authzcontext.Event, error)
	}
	ApprovalGate      hitl.ApprovalGate
	Database          db.Handle
	Store             session.Store
	Projects          project.Registry
	Sessions          *session.Host
	Settings          *settings.Service
	Invocations       invocation.Recorder
	MutationGate      *project.MutationGate
	ManagedSecrets    *secretcap.Service
	SecretIgnores     *projectignore.SecretService
	SourceLedger      *sourceledger.Store
	SourceMutations   *projectsource.SourceMutationService
	FileOperations    *fileops.Service
	EditorDocuments   *editordoc.Service
	FileBriefings     *filebriefing.Service
	Workflows         *workflow.RunManager
	WorkflowRuns      *runstate.Repository
	WorkflowComposer  *workflowcomposition.Composer
	WorkflowPersister *workflowcomposition.Persister
	Blueprints        *blueprint.Manager
	ScanCoordinator   scan.ScanCoordinator
	ScanCadence       *scancadence.Service
	PublishDetections func(*detectionpack.Matcher)
	DataDir           string
	ModuleRoot        string
	MCP               *mcp.Runtime
	ExtensionViews    *catalogview.Cache
	// ContributionReceipts and ContributionAuthority back contributed command dispatch.
	ContributionReceipts  commandinvoke.Receipts
	ContributionAuthority commandinvoke.Authority

	LLM            *llm.Service
	CostTracker    cost.CostTracker
	Events         events.ReplayHub
	EventPublisher *events.Publisher
	HostIdentity   hostidentity.Identity
	Checkpoints    hitl.CheckpointManager
	ProgressStore  progress.Store
	VisualStore    visual.Store
	HistoryStorage *historyretention.Service
	HostResources  *hostresources.Service
	HostPower      *hostpower.Controller
	Pricing        *settings.PricingHost
	AgentPresence  *agentpresence.Tracker
	Workers        worker.WorkerQueue
	WorkerCancel   *worker.CancelService
	Delegations    *delegation.Manager
	Board          *board.SnapshotBuilder
	WebResearch    webresearch.Runtime
	WebDiscoverer  webresearch.DirectDiscovererFactory
	// HarnessWorkers is filled only on the harness channel, where the host builds it.
	HarnessWorkers *harnessfixture.Workers
}

// Fill supplies every dependency d leaves unset. Services a test passes are
// kept; the ones it builds share one disposable database.
func Fill(t *testing.T, d *Deps) {
	t.Helper()
	if d.Database == nil {
		d.Database = storeDatabase(d.Store)
	}
	if d.Database == nil {
		d.Database = testdbfixture.Open(t, "api-deps.db")
	}
	if d.Store == nil {
		d.Store = sessionstore.NewMemory()
	}
	if d.Projects == nil {
		// Source ledger rows reference the project table in the same database.
		d.Projects = project.NewSQLRegistry(d.Database)
	}
	if d.ApprovalDecisions == nil {
		d.ApprovalDecisions = authzcontext.NewSQLStore(d.Database)
	}
	fillLLM(t, d)
	fillSessions(t, d)
	fillSettings(t, d)
	if d.ApprovalGate == nil {
		d.ApprovalGate = settings.NewRuleApprovalGate(d.Settings.Approvals, settings.NoSources())
	}
	if d.Invocations == nil {
		d.Invocations = invocation.NewSQLRecorder(d.Database)
	}
	if d.MutationGate == nil {
		d.MutationGate = project.NewMutationGate()
	}
	fillEvents(t, d)
	fillSources(t, d)
	fillWorkflows(t, d)
	fillScans(t, d)
	fillExtensions(t, d)
	fillHost(t, d)
	fillWorkers(t, d)
	fillWebResearch(t, d)
	fillHarness(t, d)
}

func fillWebResearch(t *testing.T, d *Deps) {
	t.Helper()
	dir := t.TempDir()
	if d.WebResearch.Catalog == nil {
		catalog, err := webresearch.LoadCatalog()
		testutil.FailErr(t, "web research catalog", err)
		d.WebResearch.Catalog = catalog
	}
	if d.WebResearch.Registry == nil {
		registry := webresearch.NewRegistry(d.WebResearch.Catalog)
		testutil.FailErr(t, "web research providers", webresearch.RegisterCatalogProviders(registry))
		d.WebResearch.Registry = registry
	}
	if d.WebResearch.Config == nil {
		d.WebResearch.Config = webresearch.NewConfigStoreAt(filepath.Join(dir, "web-research.yaml"))
	}
	if d.WebResearch.Creds == nil {
		d.WebResearch.Creds = webresearch.NewCredentialStoreAt(filepath.Join(dir, "web-research-vault.age"), d.WebResearch.Catalog)
	}
	if d.WebDiscoverer == nil {
		rt := d.WebResearch
		d.WebDiscoverer = webresearch.NewDirectDiscovererFactory(nil, rt.Registry, rt.Creds, rt.Config, rt.Catalog, decide.Reranker{})
	}
}

func fillLLM(t *testing.T, d *Deps) {
	t.Helper()
	if d.LLM != nil {
		return
	}
	dir := t.TempDir()
	catalog, err := llm.NewProviderCatalogAt(filepath.Join(dir, "providers.local.yaml"))
	testutil.FailErr(t, "provider catalog", err)
	credentials := providercredentials.NewAt(filepath.Join(dir, "credential-vault.age"))
	policy, err := llm.NewPolicyStoreAt(filepath.Join(dir, "model-policy.yaml"))
	testutil.FailErr(t, "model policy", err)
	registry, err := llm.NewRegistry(t.Context(), catalog, credentials)
	testutil.FailErr(t, "provider registry", err)
	d.LLM = &llm.Service{
		Catalog: catalog, Credentials: credentials, Registry: registry, Policy: policy,
		Router: llm.NewStaticModelRouter(policy), Mock: llm.NewMockProvider(nil),
	}
}

func fillEvents(t *testing.T, d *Deps) {
	t.Helper()
	if d.Events == nil {
		if d.EventPublisher != nil {
			if hub, ok := d.EventPublisher.Hub.(events.ReplayHub); ok {
				d.Events = hub
			}
		}
	}
	if d.Events == nil {
		d.Events = events.NewMemoryHub()
	}
	if d.EventPublisher == nil {
		d.EventPublisher = &events.Publisher{Hub: d.Events}
	}
}

func fillHost(t *testing.T, d *Deps) {
	t.Helper()
	if d.CostTracker == nil {
		d.CostTracker = d.Sessions.Coordinator.Model.Cost
	}
	if d.CostTracker == nil {
		d.CostTracker = cost.NewSQLTracker(d.Database, cost.NoopPricer{})
	}
	if d.HostIdentity.HostID == "" {
		identity, err := hostidentity.LoadOrCreate(t.TempDir())
		testutil.FailErr(t, "host identity", err)
		d.HostIdentity = identity
	}
	if d.Checkpoints == nil {
		d.Checkpoints = hitl.NewCheckpoints(hitl.NewSQLStore(d.Database), d.EventPublisher, authzcontext.SQLRecorder(d.Database))
	}
	if d.ProgressStore == nil {
		d.ProgressStore = progress.NewSQLStore(d.Database)
	}
	if d.VisualStore == nil {
		d.VisualStore = visual.NewMemoryStore()
	}
	if d.HistoryStorage == nil {
		d.HistoryStorage = historyretention.New(d.Database, filepath.Join(t.TempDir(), "store.db"), d.VisualStore)
	}
	if d.HostResources == nil {
		resources, err := hostresources.NewService(t.TempDir())
		testutil.FailErr(t, "host resources", err)
		d.HostResources = resources
	}
	if d.HostPower == nil {
		d.HostPower = hostpower.New(false)
	}
	if d.Pricing == nil {
		d.Pricing = &settings.PricingHost{Store: d.Settings.Pricing, CacheDir: t.TempDir()}
	}
	if d.AgentPresence == nil {
		d.AgentPresence = agentpresence.New(nil, nil)
	}
}

func fillWorkers(t *testing.T, d *Deps) {
	t.Helper()
	if d.Workers == nil {
		d.Workers = worker.NewInMemoryQueue(2)
	}
	if d.WorkerCancel == nil {
		d.WorkerCancel = &worker.CancelService{Queue: d.Workers, Events: d.Sessions.Coordinator.Workers, Graceful: d.Sessions.Workers.Cancel, Cancellations: d.Sessions.Workers.Cancellations}
	}
	if d.Delegations == nil {
		d.Delegations = delegation.NewManager(delegation.NewMemoryStore(), d.Workers, nil, nil)
	}
	if d.Board == nil {
		d.Board = &board.SnapshotBuilder{Delegations: d.Delegations.Store, Workers: d.Workers}
	}
	if d.Board.Git == nil {
		d.Board.Git = git.NewManager()
	}
	if d.Board.StatusCache == nil {
		d.Board.StatusCache = git.NewStatusCache(d.Board.Git)
	}
	if d.Board.Repo == nil {
		d.Board.Repo = repoinfotest.NewProvider(t)
	}
}

// fillHarness builds what the harness channel's host adds: scripted workers
// and conversation preparation.
func fillHarness(t *testing.T, d *Deps) {
	t.Helper()
	if !configdir.IsHarnessChannel() {
		return
	}
	if d.HarnessWorkers == nil {
		scripted, err := harnessfixture.NewWorkers(t.TempDir(), d.Store, d.Workers, d.Sessions.Workers.Harness.Verify,
			d.Sessions.Workers.Harness.Read, sessiondecisions.NewSQL(d.Database), refusingExecutor{})
		testutil.FailErr(t, "scripted workers", err)
		d.HarnessWorkers = scripted
	}
	if d.LLM.Preparation == nil {
		preparation, err := harnessfixture.NewPreludeController(t.TempDir(), d.Store)
		testutil.FailErr(t, "conversation preparation", err)
		d.LLM.Preparation = preparation
	}
}

// refusingExecutor runs no live workers; scripted workers cover harness tests.
type refusingExecutor struct{}

func (refusingExecutor) Execute(context.Context, wire.WorkerTask, worker.WorkerRunContext) (wire.WorkerResult, error) {
	return wire.WorkerResult{}, errors.New("live workers are not available in API tests")
}

func (refusingExecutor) AbortWorkerRuntime(context.Context, wire.WorkerTask) error { return nil }

func storeDatabase(store session.Store) db.Handle {
	if owner, ok := store.(interface{ DB() db.Handle }); ok {
		return owner.DB()
	}
	return nil
}

func fillSessions(t *testing.T, d *Deps) {
	t.Helper()
	if d.Sessions != nil {
		return
	}
	registry := tools.NewStubRegistry()
	d.Sessions = session.NewHost(d.Store, session.Models{Client: llm.NewMockProvider(nil), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, registry)
	d.Sessions.SetDataDir(t.TempDir())
	d.Sessions.SetProjectRegistry(d.Projects)
	d.Sessions.Coordinator.Guards.SetToolMetadata(testtool.RegistryInvoker{Registry: registry})
}

func fillSettings(t *testing.T, d *Deps) {
	t.Helper()
	if d.Settings == nil {
		d.Settings = &settings.Service{}
	}
	dir := t.TempDir()
	if d.Settings.TrustSurfaces == nil {
		surfaces, err := settings.NewTrustSurfacesStoreAt(filepath.Join(dir, "trust-surfaces.yaml"))
		testutil.FailErr(t, "trust surfaces store", err)
		d.Settings.TrustSurfaces = surfaces
	}
	if d.Settings.Approvals == nil {
		approvals, err := settings.NewApprovalStoreAt(filepath.Join(dir, "approvals.yaml"))
		testutil.FailErr(t, "approvals store", err)
		d.Settings.Approvals = approvals
	}
	if d.Settings.FileSummaries == nil {
		summaries, err := settings.NewFileSummariesStoreAt(filepath.Join(dir, "file-summaries.yaml"))
		testutil.FailErr(t, "file summaries store", err)
		d.Settings.FileSummaries = summaries
	}
	if d.Settings.Power == nil {
		power, err := settings.OpenPowerStore(filepath.Join(dir, "power.yaml"))
		testutil.FailErr(t, "power store", err)
		d.Settings.Power = power
	}
	if d.Settings.Limits == nil {
		limits, err := settings.NewLimitsStoreAt(filepath.Join(dir, "limits.yaml"))
		testutil.FailErr(t, "limits store", err)
		d.Settings.Limits = limits
	}
	if d.Settings.Review == nil {
		review, err := settings.NewReviewStoreAt(filepath.Join(dir, "review.yaml"))
		testutil.FailErr(t, "review store", err)
		d.Settings.Review = review
	}
	if d.Settings.Verify == nil {
		verify, err := settings.NewVerifyStoreAt(filepath.Join(dir, "verify.yaml"), filepath.Join(dir, "verify-detect.json"))
		testutil.FailErr(t, "verify store", err)
		d.Settings.Verify = verify
	}
	if d.Settings.SecurityScanners == nil {
		scanners, err := settings.NewSecurityScannersStoreAt(filepath.Join(dir, "security-scanners.yaml"))
		testutil.FailErr(t, "security scanners store", err)
		d.Settings.SecurityScanners = scanners
	}
	if d.Settings.Pricing == nil {
		pricing, err := settings.NewPricingStoreAt(filepath.Join(dir, "pricing.yaml"))
		testutil.FailErr(t, "pricing store", err)
		d.Settings.Pricing = pricing
	}
}

func fillSources(t *testing.T, d *Deps) {
	t.Helper()
	if d.ManagedSecrets == nil {
		values := credentialstore.NewEmpty(credentialstore.Slot{
			Path:      filepath.Join(t.TempDir(), credentialstore.VaultBasename),
			Namespace: credentialstore.NamespaceManagedSecrets,
			Context:   "test managed secret",
		}, func(id string) bool { _, err := uuid.Parse(id); return err == nil })
		d.ManagedSecrets = secretcap.NewWithStore(d.Database, values, nil)
	}
	if d.SecretIgnores == nil {
		d.SecretIgnores = &projectignore.SecretService{}
	}
	if d.SecretIgnores.Roots == nil {
		d.SecretIgnores.Roots = func(ctx context.Context, projectID string) ([]projectignore.Root, error) {
			p, err := d.Projects.Get(ctx, projectID)
			if err != nil {
				return nil, err
			}
			roots := make([]projectignore.Root, 0, len(p.Roots))
			for _, root := range p.Roots {
				roots = append(roots, projectignore.Root{ID: root.ID, Path: root.Path})
			}
			return roots, nil
		}
	}
	if d.SourceLedger == nil {
		d.SourceLedger = sourceledger.New(d.Database, t.TempDir())
	}
	if d.SourceMutations == nil {
		d.SourceMutations = projectsource.NewSourceMutationService(d.Database, d.SourceLedger)
	}
	if d.FileOperations == nil {
		d.FileOperations = fileops.NewService(fileops.NewStore(d.Database))
	}
	if d.EditorDocuments == nil {
		documents := editordoc.New(editordoc.NewStore(d.Database), d.SourceLedger, d.SourceLedger.History, d.Projects)
		t.Cleanup(func() { _ = documents.Close(context.Background()) })
		d.EditorDocuments = documents
	}
	if d.FileBriefings == nil {
		briefings := filebriefing.NewService(context.Background(), filebriefing.Dependencies{Store: filebriefing.NewMemory()})
		t.Cleanup(func() { briefings.Stop(); briefings.Wait(context.Background()) })
		d.FileBriefings = briefings
	}
}

func fillWorkflows(t *testing.T, d *Deps) {
	t.Helper()
	if d.WorkflowRuns == nil {
		d.WorkflowRuns = workflowpersistence.New(d.Database)
	}
	if d.Workflows == nil {
		d.Workflows = workflow.NewManager(d.WorkflowRuns, d.Store, workflowdef.NewRegistry(nil), nil)
	}
	if d.WorkflowComposer == nil {
		d.WorkflowComposer = &workflowcomposition.Composer{}
	}
	if d.WorkflowPersister == nil {
		d.WorkflowPersister = &workflowcomposition.Persister{}
	}
	if d.Blueprints == nil {
		d.Blueprints = blueprint.NewManager(blueprint.NewFileStore(func(ctx context.Context, projectID string) (string, error) {
			p, err := d.Projects.Get(ctx, projectID)
			if err != nil {
				return "", err
			}
			return project.PrimaryRootPath(p), nil
		}))
	}
}

func fillScans(t *testing.T, d *Deps) {
	t.Helper()
	if d.ScanCoordinator == nil {
		d.ScanCoordinator = scantest.Coordinator(t, scan.NewSQLStore(d.Database), nil)
	}
	if d.ScanCadence == nil {
		// An unconfigured cadence schedules nothing.
		d.ScanCadence = &scancadence.Service{}
	}
	if d.PublishDetections == nil {
		d.PublishDetections = func(*detectionpack.Matcher) {}
	}
	if d.DataDir == "" {
		d.DataDir = t.TempDir()
	}
	if d.ModuleRoot == "" {
		d.ModuleRoot = configlayout.FindModuleRoot()
	}
}

func fillExtensions(t *testing.T, d *Deps) {
	t.Helper()
	if d.MCP == nil {
		registry, err := mcp.NewRuntime(mcp.RuntimeOptions{
			StatePath:          t.TempDir(),
			GlobalOverridePath: filepath.Join(t.TempDir(), "mcp.yaml"),
			OAuthStore:         mcp.NewOAuthTokenStoreAt(filepath.Join(t.TempDir(), "mcp-oauth.vault")),
		})
		testutil.FailErr(t, "mcp registry", err)
		t.Cleanup(func() { _ = registry.Close() })
		d.MCP = registry
	}
	if d.ExtensionViews == nil {
		d.ExtensionViews = catalogview.NewCache(d.ModuleRoot, nil)
	}
	if d.ContributionReceipts == nil {
		d.ContributionReceipts = commandinvoke.SQLReceipts{DB: d.Database}
	}
	if d.ContributionAuthority == nil {
		d.ContributionAuthority = commandinvoke.PolicyAuthority{}
	}
}
