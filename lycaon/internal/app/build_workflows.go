package app

import (
	"context"
	"fmt"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	workflowblueprintfiles "github.com/lycaon/lycaon/internal/workflow/blueprintfiles"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/vocabulary"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/pkg/api"
)

func (b boardWiring) wireWorkflows() error {
	b.delegationStore = delegation.NewSQLStore(b.db)
	b.delegationStore.SetEventOutbox(b.eventOutbox)
	b.mgr.SetDelegationLegLookup(b.delegationStore)
	blueprintStore := blueprint.NewFileStore(func(ctx context.Context, projectID string) (string, error) {
		p, err := b.registry.Get(ctx, projectID)
		if err != nil {
			return "", err
		}
		return project.PrimaryRootPath(p), nil
	})
	// Blueprint grants must not begin or end unledgered.
	if b.authzCapturer == nil {
		return fmt.Errorf("authz: capturer required before blueprint approval store wiring")
	}
	b.blueprintMgr = blueprint.NewManager(blueprintStore)
	b.blueprintMgr.SetProjects(b.registry)
	b.blueprintMgr.Approvals = blueprint.NewApprovalStore(b.db, b.authzCapturer.Recorder)

	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	if err != nil {
		return fmt.Errorf("workflow manifests: %w", err)
	}
	b.manifestRegistry = manifestRegistry
	b.sessionWorkflowStore = workflowdrafts.NewSQL(b.db)
	b.manifestResolver = workflowcatalog.Resolver{
		SessionStore: b.sessionWorkflowStore,
		CatalogFor: func(ctx context.Context, _ string, sessionID string) *extpacks.EffectiveCatalog {
			if b.mgr != nil && b.store != nil && strings.TrimSpace(sessionID) != "" {
				if sess, err := b.store.Get(ctx, sessionID); err == nil && sess != nil {
					if c, err := b.mgr.Catalog().EffectiveCatalogForProject(ctx, sess.ProjectID); err == nil && c != nil {
						return c
					}
				}
			}
			// Refresh the process catalog after extension changes.
			extpacks.RefreshActiveIfStale(ctx)
			if live := extpacks.Active(); live != nil {
				return live
			}
			return b.effective
		},
		ProjectTierApplies: func(ctx context.Context, projectDir string) bool {
			return b.projectSettingsGate().AppliesPath(ctx, projectDir)
		},
	}
	b.workflowStore = workflowpersistence.New(b.db)
	b.workflowStore.Transactions.SetEventOutbox(b.eventOutbox)
	b.workflowStore.Transactions.SetSessionMutations(b.store)
	if b.authzCapturer != nil {
		b.workflowStore.Transactions.SetAuthzRecorder(b.authzCapturer.Recorder)
	}
	b.workflowMgr = workflow.NewManager(b.workflowStore, b.store, b.manifestRegistry, b.eventPub)
	b.workflowMgr.Phases.ReviewSpawnFilter = func(_ context.Context, _, _ string, candidates []string) []string {
		if b.webResearchRuntime.Config != nil && !b.webResearchRuntime.Config.SearchEnabled() {
			return agentdef.FilterExternalSourceAgents(candidates)
		}
		return candidates
	}
	b.workflowMgr.Children.ReviewSpawnFilter = b.workflowMgr.Phases.ReviewSpawnFilter
	b.workflowMgr.Recovery.Before = time.Now().UTC()
	b.workflowMgr.Verdicts.VerdictGrounding = b.mgr.EvaluateVerdictGrounding
	b.workflowMgr.Resolver.SessionStore = b.manifestResolver.SessionStore
	b.workflowMgr.Resolver.CatalogFor = b.manifestResolver.CatalogFor
	b.workflowMgr.Resolver.ProjectTierApplies = b.manifestResolver.ProjectTierApplies
	b.workflowMgr.Blueprints.Scaffold.Store = workflowpersistence.NewSessionScaffoldSQLStore(b.db)
	b.eventPub.SessionUI = session.UIWithProtection{Inner: b.workflowMgr.Presentation, Mgr: b.mgr}
	b.workflowMgr.Blueprints.Creator = blueprint.WorkflowBlueprintCreator{Manager: b.blueprintMgr}
	b.workflowMgr.Blueprints.Getter = b.blueprintMgr
	b.workflowMgr.Presentation.BlueprintGetter = b.blueprintMgr
	b.workflowMgr.Approvals.Getter = b.blueprintMgr
	workflowStore := b.workflowStore
	b.blueprintMgr.BeforeRetarget = func(ctx context.Context, projectID, from, to string) {
		run, err := workflowStore.Runs.ActiveByProjectForBlueprint(ctx, strings.TrimSpace(projectID), strings.TrimSpace(from))
		if err != nil || run == nil {
			return
		}
		b.mgr.RecordPrimaryMutation(ctx, run.SessionID, from)
		b.mgr.RecordPrimaryMutation(ctx, run.SessionID, to)
		b.mgr.RecordBlueprintBinding(ctx, run.SessionID, from)
	}
	b.blueprintMgr.AfterRetarget = b.workflowMgr.Blueprints.RebindBlueprintPath
	// A blueprint a live run executes cannot be deleted out from under it.
	b.blueprintMgr.ActiveRun = func(ctx context.Context, projectID, path string) (string, bool, error) {
		run, err := workflowStore.Runs.ActiveByProjectForBlueprint(ctx, strings.TrimSpace(projectID), strings.TrimSpace(path))
		if err != nil || run == nil {
			return "", false, err
		}
		return run.ID, true, nil
	}
	if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
		Name: "workflow-verdicts", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseServe,
		Run: b.workflowMgr.Verdicts.RecoverVerdictOperations,
	}); err != nil {
		return err
	}
	if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
		Name: "workflow-teardowns", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseServe,
		Run: b.workflowMgr.Controls.Cleanup.Recover,
	}); err != nil {
		return err
	}
	if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
		Name: "workflow-child-terminals", Kind: bootrecovery.KindReconcile, Phase: bootrecovery.PhaseServe,
		After: []string{"workflow-teardowns"}, Run: b.workflowMgr.Children.RecoverTerminalChildren,
	}); err != nil {
		return err
	}
	return b.wireWorkflowEvidence()
}

func (b boardWiring) wireWorkflowEvidence() error {
	b.evidenceStore = inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	b.simpleInspector = inspector.NewSimpleInspector(b.evidenceStore)
	b.simpleInspector.ProjectDir = func(ctx context.Context, runID string) (string, error) {
		r, err := b.delegationStore.Get(ctx, runID)
		if err != nil {
			return "", err
		}
		dir, err := project.EnsureHostDataDir(b.dataDir, r.ProjectID)
		if err != nil {
			return "", err
		}
		return dir, nil
	}
	b.workflowMgr.Verdicts.EvidenceStore = b.evidenceStore
	b.workflowMgr.SetEvidenceProjectDir(func(ctx context.Context, sessionID string) (string, error) {
		sess, err := b.store.Get(ctx, sessionID)
		if err != nil || sess == nil {
			return "", err
		}
		if strings.TrimSpace(sess.ProjectID) == "" {
			return "", nil
		}
		return project.EnsureHostDataDir(b.dataDir, sess.ProjectID)
	})
	b.workflowMgr.Verdicts.OnGateEvidencePersisted = func(ctx context.Context, sessionID, workflowRunID string, rec evidence.Record) {
		projectID, err := search.ResolveProjectIDForSession(ctx, b.db, sessionID)
		if err != nil || projectID == "" {
			return
		}
		_ = search.ProjectGateEvidenceComplete(ctx, b.db, search.ProjectGateEvidenceInput{
			ProjectID:     projectID,
			SessionID:     sessionID,
			WorkflowRunID: workflowRunID,
			Record:        rec,
		})
	}
	if b.blueprintMgr != nil {
		b.blueprintMgr.SetDataDir(b.dataDir)
	}
	// Verify gating reads run-keyed evidence and the declared test command.
	b.mgr.SetEvidenceStore(b.evidenceStore)
	if b.settingsSvc != nil {
		b.mgr.SetVerifyConfig(b.settingsSvc.Verify)
		if b.toolRuntime != nil {
			// The tool and gate share one declared-command resolver.
			b.toolRuntime.SetVerifyDeclaredCommand(b.settingsSvc.Verify.VerifyTestCommand)
		}
	}
	return b.wireWorkflowScanServices()
}

func (b boardWiring) wireWorkflowScanServices() error {
	b.gitMgr = git.NewManager()
	gitexec.SetHostConfig(gitexec.HostConfigResolver())
	b.gitStatusCache = git.NewStatusCache(b.gitMgr)
	b.gitStatusCache.RegisterRepochangeObserver()
	b.gitRepoSetCache = git.NewRepoSetCache(git.DefaultStatusCacheTTL)
	if b.toolRuntime != nil {
		b.toolRuntime.SetGitStatusCache(b.gitStatusCache)
	}

	b.gatesCfg = scancfg.DefaultGatesConfig()
	b.scanStore = scan.NewSQLStore(b.db)
	b.scanStore.SetEventOutbox(b.eventOutbox)
	b.workflowMgr.Coverage.Inventory = workflowScanInventory{store: b.scanStore}
	b.mgr.SetReportDocumentChecker(b.workflowMgr.Reports)
	b.mgr.SetScanEvidenceRuns(b.workflowMgr.Coverage)
	snapshotStore := b.sourceLedger.SnapshotStore()
	b.scanCoordinator = scan.NewCoordinator(b.scanStore, b.gitMgr, snapshotStore)
	b.scanCoordinator.Settings = b.settingsSvc.SecurityScanners
	b.securityCloseout = &scan.SecurityCloseoutChecker{
		Store:    b.scanStore,
		Evidence: b.evidenceStore,
		Settings: b.settingsSvc.SecurityScanners,
	}

	b.workerQueue = worker.NewSQLQueue(b.db, b.workersCfg.Poller.MaxConcurrency)
	b.workerQueue.SetEventOutbox(b.eventOutbox)
	b.workerQueue.SetWorkersConfig(b.workersCfg)
	b.workerQueue.SetWorkflowDomains(&worker.WorkflowDomains{Runs: b.workflowMgr.Policy, Tasks: b.workflowMgr.Fanout})
	b.workflowStore.Transactions.SetWorkerRunnableNotifier(b.workerQueue)
	if err := b.seedHostPowerWork(); err != nil {
		return err
	}
	b.scanObligation = &scan.WorkflowObligation{
		Ledger:   b.scanStore,
		Settings: b.settingsSvc.SecurityScanners,
		History:  b.scanStore,
		Runs:     b.workflowStore.Runs.Get,
		Params:   b.workflowMgr.Obligations.ObligationParams,
		Projects: func(ctx context.Context, projectID string) (string, error) {
			p, err := b.registry.Get(ctx, projectID)
			if err != nil {
				return "", err
			}
			return project.PrimaryRootPath(p), nil
		},
	}
	return b.wireWorkflowConditions()
}

func (b boardWiring) seedHostPowerWork() error {
	openWorkers, err := b.workerQueue.List(b.ctx, "", api.WorkerStatusPending, api.WorkerStatusRunning)
	if err != nil {
		return fmt.Errorf("seed host power from workers: %w", err)
	}
	for i := range openWorkers {
		b.hostPower.SetActive("worker:"+openWorkers[i].ID, true)
	}
	openScans, err := b.scanStore.ListOpen(b.ctx)
	if err != nil {
		return fmt.Errorf("seed host power from scans: %w", err)
	}
	for i := range openScans {
		b.hostPower.SetActive("scan:"+openScans[i].ID, true)
	}
	return nil
}

func (b boardWiring) wireWorkflowConditions() error {
	snapshotStore := b.sourceLedger.SnapshotStore()
	proactiveCategories := b.gatesCfg.Gates.ProactiveCategories
	if len(proactiveCategories) == 0 {
		proactiveCategories = scancfg.DefaultGatesConfig().Gates.ProactiveCategories
	}
	condReg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		DelegationStore: b.delegationStore,
		Evidence:        conditions.StoreEvidenceReader{Store: b.evidenceStore},
		ObligationResolvers: map[string]conditions.ObligationStatusReader{
			scan.WorkflowObligationKind: b.scanObligation,
		},
		BlueprintContent: func(_ context.Context, projectDir, relPath string) (string, error) {
			return workflowblueprintfiles.ReadBlueprintFile(projectDir, relPath)
		},
		DelegationCloseout:      delegation.CloseoutComplete(b.delegationStore),
		SourceVerifyPassed:      b.mgr.WorkflowSourceVerifyPassed,
		DeliveryReported:        b.mgr.WorkflowDeliveryReported,
		ScanLedger:              b.scanStore,
		SourceSnapshots:         snapshotStore,
		ScanProactiveCategories: proactiveCategories,
		SecurityScannersEnabled: func() bool { return b.settingsSvc.SecurityScanners.Effective().Enabled },
		ApprovalDenied:          b.checkpointMgr.SessionApprovalDenied,
		WorkerCycleIdle: func(projectID, sessionID, completingJobID string) (bool, error) {
			return session.ParentSessionWorkerCycleIdle(b.ctx, b.workerQueue, projectID, sessionID, completingJobID)
		},
		ChildRunStatus: func(parentRunID string) (string, bool) {
			child, err := b.workflowStore.Runs.LatestChildByParentRunID(b.ctx, parentRunID)
			if err != nil || child == nil {
				return "", false
			}
			return string(child.Status), true
		},
	})
	if err != nil {
		return fmt.Errorf("condition registry: %w", err)
	}
	b.condReg = condReg
	if err := rules.RegisterRuleConditions(b.condReg); err != nil {
		return fmt.Errorf("rule conditions: %w", err)
	}
	b.bundledRules, err = rules.LoadBundledRules()
	if err != nil {
		return fmt.Errorf("rules config: %w", err)
	}
	if err := rules.ValidatePostureRules(b.postureRegistry, sessionposture.AllSessionPostures(), b.bundledRules); err != nil {
		return fmt.Errorf("posture rules: %w", err)
	}
	ruleConfigs := make([]*rules.RulesConfig, 0, len(b.bundledRules))
	for _, cfg := range b.bundledRules {
		ruleConfigs = append(ruleConfigs, cfg)
	}
	if diags := vocabulary.ValidateBundled(b.condReg, b.manifestRegistry, ruleConfigs); len(diags) > 0 {
		return fmt.Errorf("vocabulary validation: %s", workflowdiag.Summarize(diags))
	}
	b.workflowMgr.SetConditionRegistry(b.condReg)
	if b.settingsSvc != nil && b.settingsSvc.Review != nil {
		contentApply := &toolhost.ContentApplyService{
			Mgr:    b.checkpointMgr,
			Review: b.settingsSvc.Review,
			PhaseSrc: &workflow.PhaseContentReviewSource{
				Runs: b.workflowMgr.Policy,
			},
		}
		b.toolRuntime.SetContentApply(contentApply)
	}
	if b.toolRuntime != nil && b.workflowMgr != nil {
		b.toolRuntime.SetBlueprintWriteObserver(b.workflowMgr.Blueprints)
	}
	b.workflowMgr.Obligations.Register(b.scanObligation)
	workflowTaskQuery1 := func(ctx context.Context, runID string) ([]api.WorkerTask, error) {
		return b.workerQueue.ListByWorkflowRunID(ctx, runID)
	}
	b.workflowMgr.Fanout.WorkerTasks = workflowTaskQuery1
	b.workflowMgr.Coverage.WorkerTasks = workflowTaskQuery1
	b.workflowMgr.Verdicts.Questions.WorkerTasks = workflowTaskQuery1
	b.workflowMgr.Verdicts.WorkerTasks = workflowTaskQuery1
	b.workflowMgr.Fanout.WorkerToolBudget = b.workerToolBudgetFor
	b.workflowMgr.Verdicts.Evidence.EvidenceDigests = append(b.workflowMgr.Verdicts.Evidence.EvidenceDigests, scan.WorkflowEvidenceDigest(b.scanStore))
	return b.wireWorkflowComposition()
}

func (b boardWiring) wireWorkflowComposition() error {
	obligationSpecs := workflow.ObligationSpecsFromKinds(b.workflowMgr.Obligations.Kinds)
	b.workflowComposer = &workflowcomposition.Composer{
		ModuleRoot:   b.configRoot,
		SessionStore: b.sessionWorkflowStore,
		Registry:     b.condReg,
		Obligations:  obligationSpecs,
		Agents:       b.agentRegistry,
	}
	if composePolicy, err := workflowcomposition.LoadComposePolicy(); err != nil {
		return fmt.Errorf("compose policy: %w", err)
	} else {
		b.workflowComposer.Policy = composePolicy
	}
	// Tests may replace the effective template catalog with fixtures.
	loadTemplates := func() (workflowcomposition.TemplateCatalog, error) {
		if b.cfg.TestWorkflowTemplatesDir != "" {
			return workflowcomposition.LoadTemplatesFromDir(extpacks.OnDisk(b.cfg.TestWorkflowTemplatesDir))
		}
		return workflowcomposition.LoadTemplatesEffective(b.effective)
	}
	if templates, err := loadTemplates(); err != nil {
		return fmt.Errorf("workflow templates: %w", err)
	} else {
		b.workflowComposer.Templates = templates
	}
	b.workflowPersister = &workflowcomposition.Persister{
		ModuleRoot:   b.configRoot,
		SessionStore: b.sessionWorkflowStore,
		Registry:     b.condReg,
		Obligations:  obligationSpecs,
		Agents:       b.agentRegistry,
		Policy:       b.workflowComposer.Policy,
	}

	ruleEngine, err := rules.NewPostureRuleEngine(b.postureRegistry, b.bundledRules, b.condReg)
	if err != nil {
		return fmt.Errorf("posture rule engine: %w", err)
	}
	b.ruleEngine = ruleEngine
	b.projectRulesOverlay = rules.NewProjectRulesOverlay(b.condReg)
	b.ruleEngine.Overlay = b.projectRulesOverlay
	b.ruleEngine.ProjectSettingsApply = b.projectSettingsGate().Applies
	return nil
}
