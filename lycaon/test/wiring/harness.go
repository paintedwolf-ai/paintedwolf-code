package wiring

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/app"
	"github.com/lycaon/lycaon/internal/app/configuration"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
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
)

// Harness holds a production-wired ServeApp for E2E tests.
type Harness struct {
	*app.ServeApp
	Recording *llm.RecordingClient
	Store     session.Store
	testDir   string
	dbPath    string
}

// BuildForTest assembles a host in a temporary directory.
func BuildForTest(t *testing.T, opts ...Option) *Harness {
	t.Helper()
	return BuildForTestAt(t, t.TempDir(), opts...)
}

// BuildForTestAt boots on a caller-owned directory, including an existing store.
func BuildForTestAt(t *testing.T, dir string, opts ...Option) *Harness {
	t.Helper()
	requirePreparedHostProcess(t)
	configDir := filepath.Join(dir, "config")
	testutil.FailErr(t, "create device configuration", os.MkdirAll(configDir, 0o700))
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	// Retire process-wide cached trees before removing their directory.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		testutil.FailErr(t, "clear source catalog", sourcecatalog.Process().Trees.ClearTreeStores(ctx, nil))
	})

	o := defaultOptions()
	for _, opt := range opts {
		opt(&o)
	}
	cfg := configuration.Config{TestChangeDebounce: o.changeDebounce}
	cfg.ConfigRoot = configlayout.FindModuleRoot()
	// Install packs before the builder resolves the catalog.
	installHarnessPacks(t, o.installedPackDirs)
	testDir := dir
	// Restore read-only snapshots before the directory is removed.
	t.Cleanup(func() { restoreTreePermissions(filepath.Join(testDir, "source-snapshots")) })
	dbPath := filepath.Join(testDir, "wiring-test.db")
	if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
		cloneDatabaseBaseline(t, dbPath)
	} else {
		testutil.FailErr(t, "inspect store", err)
	}
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
	if o.autoCompleteLegs {
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
	t.Cleanup(func() { testutil.FailErr(t, "close test app", sa.Close()) })

	if o.replaceManifests != nil {
		sa.Workflows.Manager.Resolver.Overlay = workflowdef.NewRegistry(o.replaceManifests)
	}

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

// CoordinatorToolContext builds a coordinator ToolContext bound to sess and dir.
func (h *Harness) CoordinatorToolContext(t *testing.T, ctx context.Context, sess *wire.Session, dir string) tools.ToolContext {
	t.Helper()
	var activeRootID string
	testutil.FailErr(t, "resolve primary root", h.DB.QueryRowContext(
		ctx,
		"SELECT id FROM project_roots WHERE project_id = ? AND is_primary = 1",
		sess.ProjectID,
	).Scan(&activeRootID))
	return tools.ToolContext{
		Identity: tools.InvocationIdentity{
			SessionID:  sess.ID,
			ProjectID:  sess.ProjectID,
			Agent:      "coordinator",
			ToolCallID: uuid.NewString(),
		},
		Source: tools.InvocationSource{
			Roots:        []projectroot.RootRef{{ID: activeRootID, Label: "root", Path: dir, IsPrimary: true}},
			ActiveRootID: activeRootID,
		},
		Host: tools.InvocationHost{
			SessionOutputDir:  t.TempDir(),
			SessionScratchDir: t.TempDir(),
		},
	}
}

// SeedProgress supplies a pending checklist that survives turn bootstrap.
func (h *Harness) SeedProgress(t *testing.T, ctx context.Context, sessionID string) {
	t.Helper()
	if h == nil || h.ToolRegistry == nil {
		t.Fatal("harness tool registry missing")
	}
	args := map[string]any{"content": "## Progress\n- [ ] wiring test plan\n"}
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sessionID}}
	_, err := h.ToolRegistry.Run(ctx, "update_progress", args, tc)
	if reject := toolrejection.AsToolReject(err); reject != nil && reject.Code == "PROGRESS_REVISION_REQUIRED" {
		// Workflow setup may already hold a checklist; replace it at its revision.
		current, ok := reject.Data["snapshot"].(progress.Snapshot)
		if !ok {
			t.Fatalf("SeedProgress: rejection carries no snapshot: %v", err)
		}
		args["expected_revision"], args["item_ids"] = current.Revision, []any{nil}
		_, err = h.ToolRegistry.Run(ctx, "update_progress", args, tc)
	}
	if err != nil {
		t.Fatalf("SeedProgress: %v", err)
	}
}

// ProgressUpdateArgs builds update_progress arguments at the session's current revision,
// keeping each existing row's identity and leaving added rows to the host.
func (h *Harness) ProgressUpdateArgs(t *testing.T, ctx context.Context, sessionID, content string) map[string]any {
	t.Helper()
	current, err := progress.NewSQLStore(h.DB).Snapshot(ctx, sessionID)
	testutil.FailErr(t, "read progress snapshot", err)
	args := map[string]any{"content": content}
	if current.Content == "" {
		return args
	}
	ids := make([]any, len(progress.ChecklistItems(content)))
	for i := range ids {
		if i < len(current.ItemIDs) {
			ids[i] = current.ItemIDs[i]
		}
	}
	args["expected_revision"], args["item_ids"] = current.Revision, ids
	return args
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

// MemoryHub returns the event hub when backed by MemoryHub, unwrapping publishers.
func (h *Harness) MemoryHub() *events.MemoryHub {
	if h == nil {
		return nil
	}
	hub := h.Events
	for hub != nil {
		if memory, ok := hub.(*events.MemoryHub); ok {
			return memory
		}
		unwrap, ok := hub.(interface{ Unwrap() events.ReplayHub })
		if !ok {
			return nil
		}
		hub = unwrap.Unwrap()
	}
	return nil
}

func mockLLMConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(configlayout.FindModuleRoot(), "config", "fixtures", "mock_llm.yaml")
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

// autoCompleteOrchestrator dispatches legs that complete immediately in mock workspaces.
func autoCompleteOrchestrator(deps orchestration.OrchestratorDeps) orchestration.Orchestrator {
	deps.Workers = &autoCompleteTopologyWorkers{TopologyWorkers: deps.Workers}
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
