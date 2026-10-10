package wiring

import (
	"context"
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/app"
	"github.com/lycaon/lycaon/internal/app/configuration"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scan"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Each test supplies its own server catalog.
func stageFakeMCPDistro(t *testing.T) {
	t.Helper()
	configtest.Overlay(t, map[config.Rel]string{
		config.DistroMCP: "providers:\n  - id: svca\n    command: \"true\"\n    args: []\n    enabled: false\n",
	})
}

// Harness holds a production-wired ServeApp for E2E tests.
type Harness struct {
	*app.ServeApp
	Recording *llm.RecordingClient
	Store     session.Store
	testDir   string
	dbPath    string
}

// BuildForTest constructs a production-parity server via app.Build with test defaults.
func BuildForTest(t *testing.T, opts ...Option) *Harness {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", api.TestAPIToken)
	t.Setenv("LYCAON_TEST", "1")
	t.Setenv("LYCAON_LOG_LEVEL", "error")
	// Isolate device configuration for deterministic tests.
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	// The process source catalog caches trees under <config>/cache; its builds
	// outlive the app, so finish and retire them before TempDir removal.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		testutil.FailErr(t, "clear source catalog", sourcecatalog.Process().Trees.ClearTreeStores(ctx, nil))
	})

	o := defaultOptions()
	for _, opt := range opts {
		opt(&o)
	}

	cfg := configuration.Config{}
	cfg.ConfigRoot = configlayout.FindModuleRoot()
	// Install packs before the builder resolves the catalog.
	installHarnessPacks(t, o.installedPackDirs)
	testDir := t.TempDir()
	// Restore read-only snapshots before TempDir cleanup.
	t.Cleanup(func() { restoreTreePermissions(filepath.Join(testDir, "source-snapshots")) })
	dbPath := filepath.Join(testDir, "wiring-test.db")
	cloneDatabaseBaseline(t, dbPath)
	cfg.DBPath = dbPath
	cfg.ListenAddr = "127.0.0.1:0"
	cfg.MockLLMPath = mockLLMConfigPath(t)
	cfg.TestMCPGlobalOverridePath = filepath.Join(testDir, "mcp-test.yaml")
	if o.mcpConnector != nil {
		cfg.TestMCPConnector = o.mcpConnector
	} else {
		cfg.TestMCPConnector = &mcp.MockConnector{Tools: map[string][]*sdkmcp.Tool{
			"svca": {{Name: "do", Description: "do"}},
		}}
	}
	stageFakeMCPDistro(t)
	if o.secretMatcher != nil {
		cfg.TestSecretMatcher = o.secretMatcher
	}
	cfg.TestDecider = o.decider
	if o.coordinatorLoopDisabled {
		lim := settings.DefaultSessionLimits()
		loop := false
		lim.CoordinatorLoop = &loop
		cfg.TestSessionLimits = &lim
	}
	// Workflow definitions use the bundled catalog; templates use deterministic fixtures.
	fixturesRoot := filepath.Join(configlayout.FindModuleRoot(), "test", "wiring", "fixtures")
	cfg.TestWorkflowTemplatesDir = filepath.Join(fixturesRoot, "workflow-templates")
	if o.useBundledScanners {
		cfg.TestAdvisoryDatabase = scantest.OSVExport(t)
	} else {
		cfg.TestScanRegistry = &scan.MockRegistry{Scanner: &scan.MockScanner{
			// Match the bundled secret scanner categories.
			CategoryList: []wire.ScanCategory{wire.ScanCategorySecret, wire.ScanCategorySecurity, wire.ScanCategorySCA},
			Result:       &scanoutput.Result{FindingsCount: 0},
		}}
	}
	if !o.productionCostPricer {
		cfg.TestCostPricer = testCostPricer{}
	}
	if o.autoCompleteDelegation {
		cfg.TestOrchestrator = autoCompleteOrchestrator
	}
	var recording *llm.RecordingClient
	switch {
	case o.llmClient != nil:
		cfg.TestLLMClient = o.llmClient
	case o.recording:
		recording = llm.NewRecordingClient(llm.NewMockProvider(loadMockConfig(t)))
		cfg.TestLLMClient = recording
	default:
		cfg.TestLLMClient = llm.NewMockProvider(loadMockConfig(t))
	}

	sa, err := app.Build(t.Context(), cfg)
	testutil.FailErr(t, "build app", err)
	t.Cleanup(func() {
		_ = sa.Close()
		_ = db.RemoveStore(dbPath)
	})
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())

	if o.replaceManifests != nil {
		sa.Workflows.Manager.Resolver.Overlay = workflowdef.NewRegistry(o.replaceManifests)
	}

	applyTestHarnessRelaxations(sa)

	trackHost(t.Name(), sa)

	harnessStore := store.NewSQL(sa.DB)
	// Seeded and runtime evidence share the same content directory.
	harnessStore.SetDataDir(testDir)
	h := &Harness{
		ServeApp:  sa,
		Recording: recording,
		Store:     harnessStore,
		testDir:   testDir,
		dbPath:    dbPath,
	}
	return h
}

// ProjectDir creates a fixture under the harness teardown root.
func (h *Harness) ProjectDir(t *testing.T, name string) string {
	t.Helper()
	if h == nil || strings.TrimSpace(h.testDir) == "" {
		t.Fatal("harness test dir not configured")
	}
	dir := filepath.Join(h.testDir, name)
	testutil.FailErr(t, "create project directory", os.MkdirAll(dir, 0o755))
	return dir
}

// DatabasePath is the harness's durable store file.
func (h *Harness) DatabasePath() string {
	return h.dbPath
}

// HostProjectDir returns the sidecar host-data tree for projectID (evidence JSONL root).
func (h *Harness) HostProjectDir(t *testing.T, projectID string) string {
	t.Helper()
	if h == nil || strings.TrimSpace(h.testDir) == "" {
		t.Fatal("harness test dir not configured")
	}
	dir, err := project.EnsureHostDataDir(h.testDir, projectID)
	testutil.FailErr(t, "ensure host project directory", err)
	return dir
}

// RegisterManifest adds or overrides a workflow manifest on the built registry.
func (h *Harness) RegisterManifest(manifest workflowdef.Manifest) {
	if h == nil || h.Workflows.Manager == nil {
		return
	}
	m := workflowdef.FinalizeManifest(manifest)
	key := m.ID + "@" + m.Version
	all := h.Workflows.Manager.Resolver.Overlay.All()
	if all == nil {
		all = map[string]workflowdef.Manifest{}
	}
	all[key] = m
	h.Workflows.Manager.Resolver.Overlay = workflowdef.NewRegistry(all)
}

// CreateHarnessSession seeds project registry rows and creates a SQL-backed session for wiring tests.
func (h *Harness) CreateHarnessSession(t *testing.T, req wire.CreateSessionRequest, dir string) (*wire.Session, error) {
	t.Helper()
	if h == nil || h.Store == nil || h.DB == nil {
		t.Fatal("harness store missing")
	}
	testdbseed.InsertProjectRoot(t, h.DB, testdbseed.DefaultProjectID, dir)
	if strings.TrimSpace(req.ProjectID) == "" {
		req.ProjectID = testdbseed.DefaultProjectID
	}
	return h.Store.Create(t.Context(), req, testdbseed.DefaultProjectID)
}

// OwnerCtx binds the harness host owner to ctx as the deciding caller.
func (h *Harness) OwnerCtx(t testing.TB, ctx context.Context) context.Context {
	t.Helper()
	return testdbseed.OwnerCaller(t, ctx, h.DB)
}

// SeedProgress supplies a pending checklist that survives turn bootstrap.
func (h *Harness) SeedProgress(t *testing.T, ctx context.Context, sessionID string) {
	t.Helper()
	if h == nil || h.ToolRegistry == nil {
		t.Fatal("harness tool registry missing")
	}
	if _, err := h.ToolRegistry.Run(ctx, "update_progress", map[string]any{
		"content": "## Progress\n- [ ] wiring test plan\n",
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: sessionID},
	}); err != nil {
		t.Fatalf("SeedProgress: %v", err)
	}
}

// StartBackgroundWorkers runs worker and scan pollers until cancellation.
func (h *Harness) StartBackgroundWorkers(t *testing.T, ctx context.Context) context.CancelFunc {
	t.Helper()
	if h == nil || h.ServeApp == nil {
		t.Fatal("harness serve app missing")
	}
	cancel, err := h.ServeApp.StartBackgroundWorkers(ctx)
	testutil.FailErr(t, "start background workers", err)
	return cancel
}

// MemoryHub returns the event hub when backed by MemoryHub.
func (h *Harness) MemoryHub() *events.MemoryHub {
	if h == nil {
		return nil
	}
	hub, _ := h.Events.(*events.MemoryHub)
	return hub
}

func mockLLMConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(configlayout.FindModuleRoot(), "config", "fixtures", "mock_llm.yaml")
}

func loadMockConfig(t *testing.T) *llm.MockConfig {
	t.Helper()
	cfg, err := llm.LoadMockConfig()
	if err != nil {
		t.Fatalf("load mock config: %v", err)
	}
	return cfg
}

type testCostPricer struct{}

// NoCharge: the fixture prices every provider, so none is local-free.
func (testCostPricer) NoCharge(_, _ string) bool { return false }

func (testCostPricer) EstimateCost(_, _ string, usage cost.TokenUsage) (cost.CostEstimate, error) {
	usd := (float64(usage.PromptTokens)/1000)*0.005 + (float64(usage.CompletionTokens)/1000)*0.015
	if usd <= 0 {
		usd = 0.01
	}
	asOf := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	return cost.CostEstimate{
		EstimatedUSD:  usd,
		Currency:      "USD",
		PricingSource: "fixture",
		PricedAsOf:    asOf,
	}, nil
}

func applyTestHarnessRelaxations(sa *app.ServeApp) {
	if sa == nil || sa.Delegations.Manager == nil {
		return
	}
	if g := sa.Delegations.Manager.Grounding; g != nil {
		cfg := g.Config
		cfg.Closeout.Mode = "off"
		g.Config = cfg
	}
}

// autoCompleteOrchestrator dispatches legs that complete immediately in mock workspaces.
func autoCompleteOrchestrator(deps orchestration.OrchestratorDeps) orchestration.Orchestrator {
	deps.Delegation = &autoCompleteDelegation{inner: deps.Delegation.(delegation.DelegationManager), store: deps.Store}
	deps.Workspaces = &mockWorkspaceBinder{}
	return orchestration.NewOrchestratorImpl(deps)
}

// restoreTreePermissions makes a materialized snapshot tree removable again.
func restoreTreePermissions(root string) {
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
}
