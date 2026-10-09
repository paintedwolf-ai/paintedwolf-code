package toolfixture

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/browser/renderhandle"
	"github.com/lycaon/lycaon/internal/call"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/parse"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/scan"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scantoolapi "github.com/lycaon/lycaon/internal/scan/toolapi"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/heldtools"
	"github.com/lycaon/lycaon/internal/tools/native/page"
	reporttools "github.com/lycaon/lycaon/internal/tools/native/reporting"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	workflowinputs "github.com/lycaon/lycaon/internal/workflow/inputs"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	workflowreview "github.com/lycaon/lycaon/internal/workflow/review"
	workflowstatetools "github.com/lycaon/lycaon/internal/workflow/statetools"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

func registerCatalogToolsOnto(t *testing.T, reg *tools.DefaultRegistry) {
	t.Helper()
	parseSvc := parse.NewDefaultService()
	if err := parse.RegisterParseTools(reg, parseSvc); err != nil {
		contractcheck.FailErr(t, "parse.RegisterParseTools failed", err)
	}
	delegationStore := delegation.NewMemoryStore()
	delegationMgr := delegation.NewManager(delegationStore, worker.NewInMemoryQueue(2), session.NewHost(store.NewMemory(), session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry()), nil)
	if err := delegation.RegisterDelegationTools(reg, delegationMgr); err != nil {
		contractcheck.FailErr(t, "delegation.RegisterDelegationTools failed", err)
	}
	sqlDB := testdbfixture.Open(t, "wf-contract.db")
	workflowMgr := workflow.NewManager(workflowpersistence.New(sqlDB), store.NewMemory(), contractcheck.CatalogRegistry(t), nil)
	if err := workflowstatetools.RegisterStateTools(reg, workflowstatetools.StateToolDeps{Runs: workflowMgr.Store.Runs, Vars: workflowMgr.Phases.Vars, Journal: workflowMgr.Phases.Journal, Resolver: &workflowMgr.Resolver, Starts: workflowMgr.Starts, Controls: workflowMgr.Controls, Scaffold: workflowMgr.Blueprints.Scaffold, Sessions: store.NewMemory()}); err != nil {
		contractcheck.FailErr(t, "workflow.RegisterStateTools failed", err)
	}
	blueprintMgr := blueprint.NewManager(blueprint.NewFileStoreForTest(t.TempDir()))
	if err := blueprint.RegisterPlanTools(reg, blueprintMgr); err != nil {
		contractcheck.FailErr(t, "blueprint.RegisterPlanTools failed", err)
	}
	boardSnap := &board.SnapshotBuilder{Repo: repotest.NewProvider(t)}
	if err := board.RegisterBoardTools(reg, board.ToolDeps{Builder: boardSnap}); err != nil {
		contractcheck.FailErr(t, "board.RegisterBoardTools failed", err)
	}
	if err := native.RegisterRecordFindingTool(reg, reporttools.RecordFindingGates{}, findings.NewMemoryStore(), func(_ context.Context, _ string) string {
		return t.TempDir()
	}); err != nil {
		contractcheck.FailErr(t, "native.RegisterRecordFindingTool failed", err)
	}
	if err := native.RegisterUpdateProgressTool(reg, progress.NewMemoryStore(), func(_ context.Context, id string) string {
		return id
	}); err != nil {
		contractcheck.FailErr(t, "native.RegisterUpdateProgressTool failed", err)
	}
	if err := native.RegisterCompleteLegTool(reg, workercompletion.CompleteLegDecoder); err != nil {
		contractcheck.FailErr(t, "native.RegisterCompleteLegTool failed", err)
	}
	if err := native.RegisterRequestDecisionTool(reg, workertools.RequestDecisionDeps{}); err != nil {
		contractcheck.FailErr(t, "native.RegisterRequestDecisionTool failed", err)
	}
	// Registration only: the budget tools never run against this ledger.
	budgetQueue, budgetLedger := worker.NewInMemoryQueue(1), worker.NewSQLBudgetLedger(nil, nil)
	if err := worker.RegisterRequestBudgetTool(reg, worker.RequestBudgetToolDeps{
		Queue: budgetQueue, Ledger: budgetLedger, Notify: func(context.Context, api.WorkerTask) {},
	}); err != nil {
		contractcheck.FailErr(t, "worker.RegisterRequestBudgetTool failed", err)
	}
	if err := worker.RegisterExtendWorkerBudgetTool(reg, worker.ExtendBudgetToolDeps{Queue: budgetQueue, Ledger: budgetLedger}); err != nil {
		contractcheck.FailErr(t, "worker.RegisterExtendWorkerBudgetTool failed", err)
	}
	if err := worker.RegisterDeclineWorkerBudgetTool(reg, worker.DeclineBudgetToolDeps{Queue: budgetQueue, Ledger: budgetLedger}); err != nil {
		contractcheck.FailErr(t, "worker.RegisterDeclineWorkerBudgetTool failed", err)
	}
	webRT, err := webresearch.WireRuntime()
	contractcheck.FailErr(t, "webresearch.WireRuntime", err)
	if err := webresearch.RegisterToolsWithFactory(reg, webresearch.ToolDeps{
		Creds: webRT.Creds, Config: webRT.Config, Catalog: webRT.Catalog, Registry: webRT.Registry,
	}, nil); err != nil {
		contractcheck.FailErr(t, "webresearch.RegisterToolsWithFactory failed", err)
	}
	templates, err := workflowcomposition.LoadTemplatesFromDir(extpacks.Bundled(config.PlatformFlows.Join("_templates")))
	contractcheck.FailErr(t, "load workflow templates", err)
	sessionWF := workflowdrafts.NewMemory()
	resolver := workflowcatalog.Resolver{SessionStore: sessionWF}
	if err := workflow.RegisterCatalogSummariesTool(reg, resolver, sessionWF, templates); err != nil {
		contractcheck.FailErr(t, "workflow.RegisterCatalogSummariesTool failed", err)
	}
	if err := workflowinputs.RegisterFeedbackTool(reg, workflowMgr.Feedback); err != nil {
		contractcheck.FailErr(t, "workflow.RegisterFeedbackTool failed", err)
	}
	if err := workflowinputs.RegisterAskUserTool(reg, workflowMgr.Asks, nil); err != nil {
		contractcheck.FailErr(t, "workflowinputs.RegisterAskUserTool failed", err)
	}
	if err := workflowphases.RegisterAdvanceTool(reg, workflowMgr.Phases); err != nil {
		contractcheck.FailErr(t, "workflowphases.RegisterAdvanceTool failed", err)
	}
	if err := workflowphases.RegisterTransitionTool(reg, workflowMgr.Phases); err != nil {
		contractcheck.FailErr(t, "workflowphases.RegisterTransitionTool failed", err)
	}
	if err := workflow.RegisterFanoutPlanTool(reg, workflowMgr.Fanout); err != nil {
		contractcheck.FailErr(t, "workflow.RegisterFanoutPlanTool failed", err)
	}
	if err := workflowreview.RegisterSubmitVerdictTool(reg, workflowMgr.Verdicts); err != nil {
		contractcheck.FailErr(t, "workflowreview.RegisterSubmitVerdictTool failed", err)
	}
	c := workflowfixture.ContractWorkflowComposer(t)
	c.Templates = templates
	if err := workflow.RegisterComposeTool(reg, c); err != nil {
		contractcheck.FailErr(t, "workflow.RegisterComposeTool failed", err)
	}
	if err := workflow.RegisterComposeFromTemplateTool(reg, c); err != nil {
		contractcheck.FailErr(t, "workflow.RegisterComposeFromTemplateTool failed", err)
	}
	persister := &workflowcomposition.Persister{
		SessionStore: sessionWF,
	}
	if err := workflow.RegisterPersistTool(reg, persister); err != nil {
		contractcheck.FailErr(t, "workflow.RegisterPersistTool failed", err)
	}
	handoffDB := testdbfixture.Open(t, "handoff-contract.db")
	callLookup := call.StoreSessionLookup{Get: func(context.Context, string) (string, error) {
		return "/tmp/project", nil
	}}
	if err := call.RegisterHandoffTools(reg, call.HandoffToolDeps{
		Calls:    call.NewSQLManager(handoffDB, callLookup),
		Sessions: callLookup,
	}); err != nil {
		contractcheck.FailErr(t, "call.RegisterHandoffTools failed", err)
	}
}

// ContractServeBootRegistry mirrors serve boot tool registration for honesty checks.
func ContractServeBootRegistry(t *testing.T) *tools.DefaultRegistry {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	configRoot := filepath.Join(root, "lycaon")
	rt, err := toolhost.NewRuntime(toolhost.RuntimeConfig{ConfigRoot: configRoot, Catalog: contractcheck.StockCatalog(t)})
	contractcheck.FailErr(t, "toolhost.NewRuntime failed", err)
	applyStockToolSchemas(t, rt)
	registerCatalogToolsOnto(t, rt.Registry)

	sqlDB := testdbfixture.Open(t, "scan-boot.db")
	coord := scantest.Coordinator(t, scan.NewSQLStore(sqlDB), nil)
	if err := scantoolapi.RegisterScanTools(rt.Registry, coord, &scan.MockRegistry{Scanner: &scan.MockScanner{}}, scancadence.New(scan.StoreFromCoordinator(coord), coord, &scan.MockRegistry{Scanner: &scan.MockScanner{}}, nil, scancfg.DefaultGatesConfig(), nil), nil, nil, nil); err != nil {
		contractcheck.FailErr(t, "scan.RegisterScanTools failed", err)
	}
	secretValues := credentialstore.NewEmpty(credentialstore.Slot{
		Path:      filepath.Join(t.TempDir(), credentialstore.VaultBasename),
		Namespace: credentialstore.NamespaceManagedSecrets,
		Context:   "contract managed secret",
	}, func(string) bool { return true })
	if err := native.RegisterSecretCapabilityTools(rt.Registry, secretcap.NewWithStore(sqlDB, secretValues, nil)); err != nil {
		contractcheck.FailErr(t, "native.RegisterSecretCapabilityTools failed", err)
	}
	if err := httpaction.Register(rt.Registry, httpaction.Deps{Boundary: rt.Boundary}); err != nil {
		contractcheck.FailErr(t, "httpaction.Register failed", err)
	}

	agents := orchestration.NewMemoryAgentRegistry()
	contractcheck.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(context.Background(), agents))
	if err := worker.RegisterTaskTool(rt.Registry, worker.TaskToolDeps{
		Queue:   worker.NewInMemoryQueue(2),
		Agents:  agents,
		Workers: worker.DefaultWorkersConfig(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := loopwake.RegisterWaitTool(rt.Registry, loopwake.NewLoopEngine(), loopwake.WaitToolDeps{}); err != nil {
		contractcheck.FailErr(t, "loopwake.RegisterWaitTool failed", err)
	}
	renderBudgets, err := browser.LoadRenderBudgets()
	contractcheck.FailErr(t, "browser.LoadRenderBudgets failed", err)
	raster := browser.NewRasterizer("", renderBudgets)
	handleStore := renderhandle.NewStore()
	if err := native.RegisterRenderViewTool(rt.Registry, rt.Boundary, raster, handleStore); err != nil {
		contractcheck.FailErr(t, "native.RegisterRenderViewTool failed", err)
	}
	if err := native.RegisterViewImageTool(rt.Registry, page.ViewImageDeps{
		Boundary:    rt.Boundary,
		Raster:      raster,
		HandleStore: handleStore,
	}); err != nil {
		contractcheck.FailErr(t, "native.RegisterViewImageTool failed", err)
	}
	if err := native.RegisterViewVideoTool(rt.Registry, page.ViewVideoDeps{Boundary: rt.Boundary, Pool: browser.NewPool(""), MaxBytes: 1 << 20}); err != nil {
		contractcheck.FailErr(t, "native.RegisterViewVideoTool failed", err)
	}
	if err := native.RegisterCapturePageTool(rt.Registry, browser.NewPool(""), nil, nil); err != nil {
		contractcheck.FailErr(t, "native.RegisterCapturePageTool failed", err)
	}
	pageReg := pagesession.NewRegistry(pagesession.DefaultConfig())
	t.Cleanup(func() { pageReg.Close(t.Context()) })
	if err := native.RegisterMeasurePageTool(rt.Registry, browser.NewPool(""), pageReg, nil); err != nil {
		contractcheck.FailErr(t, "native.RegisterMeasurePageTool failed", err)
	}
	if err := native.RegisterPageSessionTools(rt.Registry, browser.NewPool(""), pageReg, nil, nil); err != nil {
		contractcheck.FailErr(t, "native.RegisterPageSessionTools failed", err)
	}
	bgReg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	if err := native.RegisterTerminalSessionTools(rt.Registry, bgReg); err != nil {
		contractcheck.FailErr(t, "native.RegisterTerminalSessionTools failed", err)
	}
	if err := heldtools.Register(rt.Registry, heldcall.New(nil, nil)); err != nil {
		contractcheck.FailErr(t, "heldtools.Register failed", err)
	}
	memStore := store.NewMemory()
	if err := native.RegisterSurfaceNoteTool(rt.Registry, reporttools.SurfaceNoteDeps{
		Ledger: session.NewHost(memStore, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry()).Verification.Evidence,
		Messages: func(ctx context.Context, sessionID string) ([]api.Message, error) {
			return memStore.GetMessages(ctx, sessionID)
		},
	}); err != nil {
		contractcheck.FailErr(t, "native.RegisterSurfaceNoteTool failed", err)
	}
	if err := native.RegisterRecallTool(rt.Registry, recall.NewService(sqlDB, t.TempDir())); err != nil {
		contractcheck.FailErr(t, "native.RegisterRecallTool failed", err)
	}
	return rt.Registry
}

// RegisterCatalogToolsForContract registers the catalog tools with their
// shipped metadata, as the serving registry does.
func RegisterCatalogToolsForContract(t *testing.T) *tools.DefaultRegistry {
	t.Helper()
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(contractcheck.StockCatalog(t))
	contractcheck.FailErr(t, "LoadEffectiveToolSchemas", err)
	reg, err := tools.NewCatalogRegistry(schemas)
	contractcheck.FailErr(t, "NewCatalogRegistry", err)
	registerCatalogToolsOnto(t, reg)
	return reg
}

func BootRegisteredToolSet(t *testing.T, reg *tools.DefaultRegistry) map[string]bool {
	t.Helper()
	registered, err := tools.BootRegisteredToolSet(reg)
	contractcheck.FailErr(t, "tools.BootRegisteredToolSet failed", err)
	return registered
}
