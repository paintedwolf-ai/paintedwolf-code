package session_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

type contextualToolsFixture struct {
	Mgr        *session.Manager
	Store      session.Store
	Workflow   *workflow.RunManager
	Executor   *tools.DefaultToolExecutor
	Sess       *api.Session
	ProfileID  string
	ProjectDir string
}

func setupContextualToolsFixture(t *testing.T, posture api.SessionPosture) contextualToolsFixture {
	return setupContextualToolsFixtureFull(t, posture, settings.DefaultSessionLimits(), nil)
}

func setupContextualToolsFixtureWithLLM(t *testing.T, posture api.SessionPosture, client modelcall.LLMClient) contextualToolsFixture {
	return setupContextualToolsFixtureFull(t, posture, settings.DefaultSessionLimits(), client)
}

func setupContextualToolsFixtureFull(t *testing.T, posture api.SessionPosture, cfg settings.SessionLimits, client modelcall.LLMClient) contextualToolsFixture {
	t.Helper()
	t.Setenv("LYCAON_LLM_MOCK", "1")
	root := configlayout.FindModuleRoot()
	configRoot := root

	sqlDB := testdbfixture.Open(t, "contextual-tools.db")

	store := store.NewSQL(sqlDB)
	rt := newContextualToolsRuntime(t, configRoot)
	mgr := session.NewManager(store, client, tools.NewStubRegistry(), cfg)
	oartest.InstallCloseoutPolicy(t, mgr)
	mgr.SetProjectRegistry(project.NewSQLRegistry(sqlDB))
	mgr.SetToolInvoker(rt.Executor)
	wireBundledToolPolicyForTest(t, mgr)

	agents := orchestration.NewMemoryAgentRegistry()
	if err := orchestration.LoadRequiredAgentRegistry(context.Background(), agents); err != nil {
		testutil.FailErr(t, "LoadRequiredAgentRegistry", err)
	}
	mgr.SetAgentRegistry(agents)

	bundledDir := filepath.Join(configRoot, "config", "packs", "painted-wolf", "platform", "workflows")
	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "workflow.RegistryFromDirs failed", err)
	sessionWF := workflow.NewSessionWorkflowSQLStore(sqlDB)
	workflowMgr := workflow.NewManager(workflow.NewSQLStore(sqlDB), store, manifestRegistry, nil)
	workflowMgr.Resolver = workflow.ManifestResolver{SessionStore: sessionWF}
	workflowMgr.SessionScaffold = workflow.NewSessionScaffoldSQLStore(sqlDB)
	projectDir := t.TempDir()
	blueprintStore := blueprint.NewFileStoreForTest(projectDir)
	blueprintMgr := blueprint.NewManager(blueprintStore)
	workflowMgr.BlueprintCreate = blueprint.WorkflowBlueprintCreator{Manager: blueprintMgr}
	workflowMgr.BlueprintGet = blueprintMgr
	mgr.SetWorkflowSessionView(workflowMgr)
	mgr.SetCoordinatorTurnFrameSource(&workflow.CoordinatorTurnFrameLoader{Runs: workflowMgr, SessionStore: sessionWF})
	if err := workflow.RegisterStateTools(rt.Registry, workflow.StateToolDeps{Runs: workflowMgr, Sessions: store}); err != nil {
		testutil.FailErr(t, "workflow.RegisterStateTools failed", err)
	}
	if err := workflow.RegisterAdvanceTool(rt.Registry, workflowMgr); err != nil {
		testutil.FailErr(t, "workflow.RegisterAdvanceTool failed", err)
	}
	if err := workflow.RegisterTransitionTool(rt.Registry, workflowMgr); err != nil {
		testutil.FailErr(t, "workflow.RegisterTransitionTool failed", err)
	}
	if err := workflow.RegisterFeedbackTool(rt.Registry, workflowMgr); err != nil {
		testutil.FailErr(t, "workflow.RegisterFeedbackTool failed", err)
	}
	if err := workflow.RegisterAskUserTool(rt.Registry, workflowMgr, rt.Boundary); err != nil {
		testutil.FailErr(t, "workflow.RegisterAskUserTool failed", err)
	}
	registerContextualToolsCoordinatorExtras(t, configRoot, rt.Registry, mgr, agents, sessionWF, bundledDir)

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: posture}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	if err := mgr.SetAgentType(ctx, sess.ID, orchestration.ProfileCoordinator); err != nil {
		testutil.FailErr(t, "mgr.SetAgentType failed", err)
	}
	sess.AgentType = orchestration.ProfileCoordinator

	return contextualToolsFixture{
		Mgr:        mgr,
		Store:      store,
		Workflow:   workflowMgr,
		Executor:   rt.Executor,
		ProfileID:  "coordinator",
		Sess:       sess,
		ProjectDir: projectDir,
	}
}

func wireBundledToolPolicyForTest(t *testing.T, mgr *session.Manager) {
	t.Helper()
	postures, err := session.LoadPostureRegistry()
	testutil.FailErr(t, "LoadPostureRegistry", err)
	packs, err := rules.LoadBundledRules()
	testutil.FailErr(t, "LoadBundledRules", err)
	if err := rules.ValidatePostureRules(postures, session.AllSessionPostures(), packs); err != nil {
		testutil.FailErr(t, "ValidatePostureRules", err)
	}
	condReg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "NewDefaultRegistry", err)
	if err := rules.RegisterRuleConditions(condReg); err != nil {
		testutil.FailErr(t, "RegisterRuleConditions", err)
	}
	engine, err := rules.NewPostureRuleEngine(postures, packs, condReg)
	testutil.FailErr(t, "NewPostureRuleEngine", err)
	mgr.SetPostureRegistry(postures)
	mgr.SetRuleEngine(engine)
}

// wirePromptTestManager wires toolhost + posture rules so coordinator Prompt has visible_tools.
func wirePromptTestManager(t *testing.T, mgr *session.Manager) {
	t.Helper()
	oartest.InstallCloseoutPolicy(t, mgr)
	configRoot := configlayout.FindModuleRoot()
	rt := newContextualToolsRuntime(t, configRoot)
	mgr.SetToolInvoker(rt.Executor)
	wireBundledToolPolicyForTest(t, mgr)
}

func newContextualToolsRuntime(t *testing.T, configRoot string) *toolhost.Runtime {
	t.Helper()
	content, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "extpacks.DiscoverStockContent", err)
	effective := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   content,
		Desired: extpacks.EmptyDesired(),
	})
	_, diagnostics, err := extpacks.LoadEffectiveToolSchemas(effective)
	testutil.FailErr(t, "extpacks.LoadEffectiveToolSchemas", err)
	if len(diagnostics) != 0 {
		t.Fatalf("tool schema diagnostics=%v", diagnostics)
	}
	runtime, err := toolhost.NewRuntime(toolhost.RuntimeConfig{ConfigRoot: configRoot, Catalog: effective})
	testutil.FailErr(t, "toolhost.NewRuntime", err)
	return runtime
}

func registerContextualToolsCoordinatorExtras(
	t *testing.T,
	configRoot string,
	reg *tools.DefaultRegistry,
	mgr *session.Manager,
	agents *orchestration.MemoryAgentRegistry,
	sessionWF *workflow.SessionWorkflowSQLStore,
	bundledDir string,
) {
	t.Helper()
	delegStore := delegation.NewMemoryStore()
	queue := worker.NewInMemoryQueue(4)
	delegMgr := delegation.NewManager(delegStore, queue, mgr, delegation.AllowGate{})
	if err := delegation.RegisterDelegationTools(reg, delegMgr); err != nil {
		testutil.FailErr(t, "delegation.RegisterDelegationTools failed", err)
	}
	if err := worker.RegisterTaskTool(reg, worker.TaskToolDeps{
		Sessions: mgr,
		Queue:    queue,
		Agents:   agents,
		Workers:  worker.DefaultWorkersConfig(),
	}); err != nil {
		t.Fatal(err)
	}
	condReg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	policy, err := workflow.LoadComposePolicy()
	testutil.FailErr(t, "workflow.LoadComposePolicy failed", err)
	templates, err := workflow.LoadTemplatesFromDir(extpacks.Bundled(config.PlatformFlows.Join("_templates")))
	testutil.FailErr(t, "load workflow templates", err)
	composer := &workflow.Composer{
		SessionStore: sessionWF,
		Registry:     condReg,
		Agents:       agents,
		Policy:       policy,
		Templates:    templates,
	}
	if err := workflow.RegisterComposeTool(reg, composer); err != nil {
		testutil.FailErr(t, "workflow.RegisterComposeTool failed", err)
	}
	if err := workflow.RegisterComposeFromTemplateTool(reg, composer); err != nil {
		testutil.FailErr(t, "workflow.RegisterComposeFromTemplateTool failed", err)
	}
	if err := workflow.RegisterCatalogSummariesTool(reg, workflow.ManifestResolver{
		SessionStore: sessionWF,
	}, sessionWF, composer.Templates); err != nil {
		t.Fatal(err)
	}
	persister := &workflow.Persister{SessionStore: sessionWF}
	if err := workflow.RegisterPersistTool(reg, persister); err != nil {
		testutil.FailErr(t, "workflow.RegisterPersistTool failed", err)
	}
}

func toolNames(metas []tools.ToolMeta) []string {
	out := make([]string, len(metas))
	for i, meta := range metas {
		out[i] = meta.Name
	}
	return out
}

func hasTool(metas []tools.ToolMeta, name string) bool {
	for _, meta := range metas {
		if meta.Name == name {
			return true
		}
	}
	return false
}
