package contract

import (
	workflowruntime "github.com/lycaon/lycaon/internal/workflow/runtime"
	workflowstatetools "github.com/lycaon/lycaon/internal/workflow/statetools"

	"context"
	workflowinputs "github.com/lycaon/lycaon/internal/workflow/inputs"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

type postureToolExpectation struct {
	posture api.SessionPosture
	include []string
	exclude []string
	withRun bool
}

// frozenPostureToolMatrix is the SSOT for contextual tool surface expectations.
var frozenPostureToolMatrix = []postureToolExpectation{
	// Profile grant; the turn surface gates invoke.
	{
		posture: api.SessionPostureSpec,
		include: []string{"read", "write", "edit", "grep", "workflow_compose", "command"},
		exclude: []string{"delegate_dispatch"},
	},
	{
		posture: api.SessionPostureBuild,
		include: []string{"delegate_dispatch", "task", "read", "grep", "write", "state_start", "workflow_advance", "workflow_transition", "command"},
	},
	{
		posture: api.SessionPostureBuild,
		withRun: true,
		include: []string{"read", "write", "edit", "task", "state_start", "workflow_advance", "workflow_transition", "command"},
	},
}

func TestFrozenPostureToolMatrix(t *testing.T) {
	for _, row := range frozenPostureToolMatrix {
		name := string(row.posture)
		if row.withRun {
			name += "+active_run"
		}
		t.Run(name, func(t *testing.T) {
			listed := listCoordinatorToolsForPosture(t, row)
			for _, tool := range row.include {
				if !containsToolName(listed, tool) {
					t.Fatalf("expected %q in listed tools: %v", tool, listed)
				}
			}
			for _, tool := range row.exclude {
				if containsToolName(listed, tool) {
					t.Fatalf("expected %q absent from listed tools: %v", tool, listed)
				}
			}
		})
	}
}

func TestEvaluateToolRulesNotInSessionPackage(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	sessionDir := filepath.Join(root, "lycaon", "internal", "session")
	entries, err := os.ReadDir(sessionDir)
	contractcheck.FailErr(t, "read directory entries", err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(sessionDir, e.Name()))
		contractcheck.FailErr(t, "read file", err)
		if strings.Contains(string(data), "evaluateToolRules") {
			t.Fatalf("session/%s must not define evaluateToolRules", e.Name())
		}
	}
}

func listCoordinatorToolsForPosture(t *testing.T, row postureToolExpectation) []string {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	configRoot := filepath.Join(root, "lycaon")
	sqlDB := testdbfixture.Open(t, "contextual-matrix.db")

	store := store.NewSQL(sqlDB)
	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	rt, err := toolhost.NewRuntime(toolhost.RuntimeConfig{ConfigRoot: configRoot, Catalog: contractcheck.StockCatalog(t)})
	contractcheck.FailErr(t, "toolhost.NewRuntime failed", err)
	mgr := session.NewManager(store, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetToolInvoker(rt.Executor)
	workflowMgr := wireToolpolicyMatrixContract(t, configRoot, mgr, store, rt.Registry, sqlDB)

	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: row.posture}, testdbseed.DefaultProjectID)
	contractcheck.FailErr(t, "create session in store", err)
	if row.withRun {
		if _, err := workflowMgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{WorkflowID: "plan", WorkflowVersion: "1.0.0"}); err != nil {
			contractcheck.FailErr(t, "workflowMgr.Starts.StartHuman failed", err)
		}
	}
	names := make([]string, 0)
	for _, meta := range mgr.PromptToolPolicy().ListForPrompt(ctx, sess, "coordinator") {
		names = append(names, meta.Name)
	}
	return names
}

func wireToolpolicyMatrixContract(
	t *testing.T,
	configRoot string,
	mgr *session.Manager,
	store session.Store,
	reg *tools.DefaultRegistry,
	sqlDB db.Handle,
) *workflow.RunManager {
	t.Helper()
	projectDir := t.TempDir()
	postures, err := session.LoadPostureRegistry()
	contractcheck.FailErr(t, "session.LoadPostureRegistry failed", err)
	packs, err := rules.LoadBundledRules()
	contractcheck.FailErr(t, "rules.LoadBundledRules failed", err)
	condReg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	if err := rules.RegisterRuleConditions(condReg); err != nil {
		contractcheck.FailErr(t, "register rule conditions", err)
	}
	engine, err := rules.NewPostureRuleEngine(postures, packs, condReg)
	contractcheck.FailErr(t, "rules.NewPostureRuleEngine failed", err)
	mgr.SetPostureRegistry(postures)
	mgr.SetRuleEngine(engine)

	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)
	mgr.SetAgentRegistry(agents)

	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "workflow.RegistryFromDirs failed", err)
	sessionWF := workflowdrafts.NewSQL(sqlDB)
	blueprintStore := blueprint.NewFileStoreForTest(projectDir)
	blueprintMgr := blueprint.NewManager(blueprintStore)
	workflowMgr := workflow.NewManager(workflowpersistence.New(sqlDB), store, manifestRegistry, nil)
	workflowMgr.Resolver.SessionStore = sessionWF
	workflowMgr.Blueprints.Creator = blueprint.WorkflowBlueprintCreator{Manager: blueprintMgr}
	workflowMgr.Blueprints.Getter = blueprintMgr
	workflowMgr.Presentation.BlueprintGetter = blueprintMgr
	workflowMgr.Approvals.Getter = blueprintMgr
	mgr.SetWorkflowDomains(&session.WorkflowDomains{Runs: workflowMgr.Store.Runs, Policy: workflowMgr.Policy, Ambient: workflowMgr.Ambient, Blueprints: workflowMgr.Blueprints, Batch: workflowMgr.Batch, Slash: workflowMgr.Slash, Requests: workflowMgr.Requests, Feedback: workflowMgr.Feedback, Transcript: workflowMgr.Transcript, Asks: workflowMgr.Asks, Fanout: workflowMgr.Fanout, Phases: workflowMgr.Phases, Reports: workflowMgr.Reports, Recovery: workflowMgr.Recovery, Cleanup: workflowMgr})
	mgr.SetCoordinatorTurnFrameSource(&workflowruntime.CoordinatorFrames{Runs: workflowMgr.Store.Runs, Resolver: &workflowMgr.Resolver, Snapshots: workflowMgr.Snapshots, Policy: workflowMgr.Policy, Obligations: workflowMgr.Obligations, SessionStore: sessionWF})
	if err := workflowstatetools.RegisterStateTools(reg, workflowstatetools.StateToolDeps{Runs: workflowMgr.Store.Runs, Vars: workflowMgr.Phases.Vars, Journal: workflowMgr.Phases.Journal, Resolver: &workflowMgr.Resolver, Starts: workflowMgr.Starts, Controls: workflowMgr.Controls, Scaffold: workflowMgr.Blueprints.Scaffold, Sessions: store}); err != nil {
		contractcheck.FailErr(t, "workflow.RegisterStateTools failed", err)
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
	if err := workflowinputs.RegisterFeedbackTool(reg, workflowMgr.Feedback); err != nil {
		contractcheck.FailErr(t, "workflow.RegisterFeedbackTool failed", err)
	}
	if err := workflowinputs.RegisterAskUserTool(reg, workflowMgr.Asks, nil); err != nil {
		contractcheck.FailErr(t, "workflowinputs.RegisterAskUserTool failed", err)
	}

	delegStore := delegation.NewMemoryStore()
	queue := worker.NewInMemoryQueue(4)
	delegMgr := delegation.NewManager(delegStore, queue, mgr, delegation.AllowGate{})
	if err := delegation.RegisterDelegationTools(reg, delegMgr); err != nil {
		contractcheck.FailErr(t, "delegation.RegisterDelegationTools failed", err)
	}
	if err := worker.RegisterTaskTool(reg, worker.TaskToolDeps{Sessions: mgr, Queue: queue, Agents: agents, Workers: worker.DefaultWorkersConfig()}); err != nil {
		contractcheck.FailErr(t, "worker.RegisterTaskTool failed", err)
	}
	policy, err := workflowcomposition.LoadComposePolicy()
	contractcheck.FailErr(t, "workflowcomposition.LoadComposePolicy failed", err)
	templates, err := workflowcomposition.LoadTemplatesFromDir(extpacks.Bundled(config.PlatformFlows.Join("_templates")))
	contractcheck.FailErr(t, "load workflow templates", err)
	composer := &workflowcomposition.Composer{SessionStore: sessionWF, Registry: condReg, Agents: agents, Policy: policy, Templates: templates}
	if err := workflow.RegisterComposeTool(reg, composer); err != nil {
		contractcheck.FailErr(t, "workflow.RegisterComposeTool failed", err)
	}
	if err := workflow.RegisterComposeFromTemplateTool(reg, composer); err != nil {
		contractcheck.FailErr(t, "workflow.RegisterComposeFromTemplateTool failed", err)
	}
	if err := workflow.RegisterCatalogSummariesTool(reg, workflowcatalog.Resolver{
		SessionStore: sessionWF,
	}, sessionWF, templates); err != nil {
		contractcheck.FailErr(t, "workflow.RegisterCatalogSummariesTool failed", err)
	}
	persister := &workflowcomposition.Persister{SessionStore: sessionWF}
	if err := workflow.RegisterPersistTool(reg, persister); err != nil {
		contractcheck.FailErr(t, "workflow.RegisterPersistTool failed", err)
	}
	return workflowMgr
}

func containsToolName(names []string, tool string) bool {
	for _, name := range names {
		if name == tool {
			return true
		}
	}
	return false
}
