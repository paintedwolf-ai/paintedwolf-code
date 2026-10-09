package app

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/projectcontrib"
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
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/pkg/api"
)

func (b boardWiring) wireWorkflows() error {
	b.delegationStore = delegation.NewSQLStore(b.storage.Database)
	b.delegationStore.SetEventOutbox(b.events.Outbox)
	b.mgr.SetDelegationLegLookup(b.delegationStore)
	blueprintStore := blueprint.NewFileStore(func(ctx context.Context, projectID string) (string, error) {
		p, err := b.storage.Projects.Get(ctx, projectID)
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
	b.blueprintMgr.SetProjects(b.storage.Projects)
	b.blueprintMgr.Approvals = blueprint.NewApprovalStore(b.storage.Database, b.authzCapturer.Recorder)

	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	if err != nil {
		return fmt.Errorf("workflow manifests: %w", err)
	}
	b.manifestRegistry = manifestRegistry
	b.sessionWorkflowStore = workflow.NewSessionWorkflowSQLStore(b.storage.Database)
	b.manifestResolver = workflow.ManifestResolver{
		SessionStore: b.sessionWorkflowStore,
		CatalogFor: func(ctx context.Context, _ string, sessionID string) *extpacks.EffectiveCatalog {
			if b.mgr != nil && b.storage.Sessions != nil && strings.TrimSpace(sessionID) != "" {
				if sess, err := b.storage.Sessions.Get(ctx, sessionID); err == nil && sess != nil {
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
			return b.catalog.Effective
		},
		ProjectTierApplies: func(ctx context.Context, projectDir string) bool {
			return b.settings.ProjectSurfaceGate(projectcontrib.SurfaceProjectSettings, b.storage.Projects).AppliesPath(ctx, projectDir)
		},
	}
	b.workflowStore = workflow.NewSQLStore(b.storage.Database)
	b.workflowStore.SetEventOutbox(b.events.Outbox)
	b.workflowStore.SetSessionMutations(b.storage.Sessions)
	if b.authzCapturer != nil {
		b.workflowStore.SetAuthzRecorder(b.authzCapturer.Recorder)
	}
	b.workflowMgr = workflow.NewManager(b.workflowStore, b.storage.Sessions, b.manifestRegistry, b.events.Publisher)
	b.workflowMgr.ReviewSpawnFilter = func(_ context.Context, _, _ string, candidates []string) []string {
		if b.webResearchRuntime.Config != nil && !b.webResearchRuntime.Config.SearchEnabled() {
			return agentdef.FilterExternalSourceAgents(candidates)
		}
		return candidates
	}
	b.workflowMgr.OrphanReconcileBefore = time.Now().UTC()
	b.workflowMgr.VerdictGrounding = b.mgr.EvaluateVerdictGrounding
	b.workflowMgr.Resolver = b.manifestResolver
	b.workflowMgr.SessionScaffold = workflow.NewSessionScaffoldSQLStore(b.storage.Database)
	b.events.Publisher.SessionUI = session.UIWithProtection{Inner: b.workflowMgr, Mgr: b.mgr}
	b.workflowMgr.BlueprintCreate = blueprint.WorkflowBlueprintCreator{Manager: b.blueprintMgr}
	b.workflowMgr.BlueprintGet = b.blueprintMgr
	workflowStore := b.workflowStore
	b.blueprintMgr.BeforeRetarget = func(ctx context.Context, projectID, from, to string) {
		run, err := workflowStore.ActiveByProjectForBlueprint(ctx, strings.TrimSpace(projectID), strings.TrimSpace(from))
		if err != nil || run == nil {
			return
		}
		b.mgr.RecordPrimaryMutation(ctx, run.SessionID, from)
		b.mgr.RecordPrimaryMutation(ctx, run.SessionID, to)
		b.mgr.RecordBlueprintBinding(ctx, run.SessionID, from)
	}
	b.blueprintMgr.AfterRetarget = b.workflowMgr.RebindBlueprintPath
	// A blueprint a live run executes cannot be deleted out from under it.
	b.blueprintMgr.ActiveRun = func(ctx context.Context, projectID, path string) (string, bool, error) {
		run, err := workflowStore.ActiveByProjectForBlueprint(ctx, strings.TrimSpace(projectID), strings.TrimSpace(path))
		if err != nil || run == nil {
			return "", false, err
		}
		return run.ID, true, nil
	}
	if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
		Name: "workflow-verdicts", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseServe,
		Run: b.workflowMgr.RecoverVerdictOperations,
	}); err != nil {
		return err
	}
	if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
		Name: "workflow-teardowns", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseServe,
		Run: b.workflowMgr.RecoverTeardownOperations,
	}); err != nil {
		return err
	}
	if err := delegationWiring(b).registerRecovery(bootrecovery.Entry{
		Name: "workflow-child-terminals", Kind: bootrecovery.KindReconcile, Phase: bootrecovery.PhaseServe,
		After: []string{"workflow-teardowns"}, Run: b.workflowMgr.RecoverTerminalChildren,
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
		dir, err := project.EnsureHostDataDir(b.storage.Directory, r.ProjectID)
		if err != nil {
			return "", err
		}
		return dir, nil
	}
	b.workflowMgr.EvidenceStore = b.evidenceStore
	b.workflowMgr.EvidenceProjectDir = func(ctx context.Context, sessionID string) (string, error) {
		sess, err := b.storage.Sessions.Get(ctx, sessionID)
		if err != nil || sess == nil {
			return "", err
		}
		if strings.TrimSpace(sess.ProjectID) == "" {
			return "", nil
		}
		return project.EnsureHostDataDir(b.storage.Directory, sess.ProjectID)
	}
	b.workflowMgr.OnGateEvidencePersisted = func(ctx context.Context, sessionID, workflowRunID string, rec evidence.Record) {
		projectID, err := search.ResolveProjectIDForSession(ctx, b.storage.Database, sessionID)
		if err != nil || projectID == "" {
			return
		}
		_ = search.ProjectGateEvidenceComplete(ctx, b.storage.Database, search.ProjectGateEvidenceInput{
			ProjectID:     projectID,
			SessionID:     sessionID,
			WorkflowRunID: workflowRunID,
			Record:        rec,
		})
	}
	if b.blueprintMgr != nil {
		b.blueprintMgr.SetDataDir(b.storage.Directory)
	}
	// Verify gating reads run-keyed evidence and the declared test command.
	b.mgr.SetEvidenceStore(b.evidenceStore)
	if b.settings.Service != nil {
		b.mgr.SetVerifyConfig(b.settings.Service.Verify)
		if b.toolRuntime != nil {
			// The tool and gate share one declared-command resolver.
			b.toolRuntime.Commands.SetVerifyDeclaredCommand(b.settings.Service.Verify.VerifyTestCommand)
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
		b.toolRuntime.Survey.SetGitStatusCache(b.gitStatusCache)
	}

	b.gatesCfg = scancfg.DefaultGatesConfig()
	b.scanStore = scan.NewSQLStore(b.storage.Database)
	b.scanStore.SetEventOutbox(b.events.Outbox)
	b.workflowMgr.Inventory = workflowScanInventory{store: b.scanStore}
	b.mgr.SetReportDocumentChecker(b.workflowMgr)
	b.mgr.SetScanEvidenceRuns(b.workflowMgr)
	snapshotStore := b.storage.SourceLedger.SnapshotStore()
	b.scanCoordinator = scan.NewCoordinator(b.scanStore, b.gitMgr, snapshotStore)
	b.scanCoordinator.Settings = b.settings.Service.SecurityScanners
	b.securityCloseout = &scan.SecurityCloseoutChecker{
		Store:    b.scanStore,
		Evidence: b.evidenceStore,
		Settings: b.settings.Service.SecurityScanners,
	}

	b.workerQueue = worker.NewSQLQueue(b.storage.Database, b.workersCfg.Poller.MaxConcurrency)
	b.events.BindWorkers(b.workerQueue)
	b.workerQueue.SetEventOutbox(b.events.Outbox)
	b.workerQueue.SetWorkersConfig(b.workersCfg)
	b.workerQueue.SetWorkflowRunChecker(b.workflowMgr)
	b.workflowStore.SetWorkerRunnableNotifier(b.workerQueue)
	if err := b.seedHostPowerWork(); err != nil {
		return err
	}
	b.scanObligation = &scan.WorkflowObligation{
		Ledger:   b.scanStore,
		Settings: b.settings.Service.SecurityScanners,
		History:  b.scanStore,
		Runs:     b.workflowStore.Get,
		Params:   b.workflowMgr.ObligationParams,
		Projects: func(ctx context.Context, projectID string) (string, error) {
			p, err := b.storage.Projects.Get(ctx, projectID)
			if err != nil {
				return "", err
			}
			return project.PrimaryRootPath(p), nil
		},
	}
	return b.wireWorkflowConditions()
}

func (b boardWiring) seedHostPowerWork() error {
	openWorkers, err := b.workerQueue.List(b.startup.ctx, "", api.WorkerStatusPending, api.WorkerStatusRunning)
	if err != nil {
		return fmt.Errorf("seed host power from workers: %w", err)
	}
	for i := range openWorkers {
		b.settings.Power.SetActive("worker:"+openWorkers[i].ID, true)
	}
	openScans, err := b.scanStore.ListOpen(b.startup.ctx)
	if err != nil {
		return fmt.Errorf("seed host power from scans: %w", err)
	}
	for i := range openScans {
		b.settings.Power.SetActive("scan:"+openScans[i].ID, true)
	}
	return nil
}

func (b boardWiring) wireWorkflowConditions() error {
	snapshotStore := b.storage.SourceLedger.SnapshotStore()
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
			return workflow.ReadBlueprintFile(projectDir, relPath)
		},
		DelegationCloseout:      delegation.CloseoutComplete(b.delegationStore),
		SourceVerifyPassed:      b.mgr.WorkflowSourceVerifyPassed,
		DeliveryReported:        b.mgr.WorkflowDeliveryReported,
		ScanLedger:              b.scanStore,
		SourceSnapshots:         snapshotStore,
		ScanProactiveCategories: proactiveCategories,
		SecurityScannersEnabled: func() bool { return b.settings.Service.SecurityScanners.Effective().Enabled },
		ApprovalDenied:          b.checkpointMgr.SessionApprovalDenied,
		WorkerCycleIdle: func(projectID, sessionID, completingJobID string) (bool, error) {
			return session.ParentSessionWorkerCycleIdle(b.startup.ctx, b.workerQueue, projectID, sessionID, completingJobID)
		},
		ChildRunStatus: func(parentRunID string) (string, bool) {
			child, err := b.workflowStore.LatestChildByParentRunID(b.startup.ctx, parentRunID)
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
	if err := rules.ValidatePostureRules(b.agents.Postures, session.AllSessionPostures(), b.bundledRules); err != nil {
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
	if b.settings.Service != nil && b.settings.Service.Review != nil {
		contentApply := &toolhost.ContentApplyService{
			Mgr:    b.checkpointMgr,
			Review: b.settings.Service.Review,
			PhaseSrc: &workflow.PhaseContentReviewSource{
				Runs: b.workflowMgr,
			},
		}
		b.toolRuntime.Mutations.SetContentApply(contentApply)
	}
	if b.toolRuntime != nil && b.workflowMgr != nil {
		b.toolRuntime.Mutations.SetBlueprintWriteObserver(b.workflowMgr)
	}
	b.workflowMgr.RegisterObligationKind(b.scanObligation)
	b.workflowMgr.WorkerTasks = func(ctx context.Context, runID string) ([]api.WorkerTask, error) {
		return b.workerQueue.ListByWorkflowRunID(ctx, runID)
	}
	b.workflowMgr.WorkerToolBudget = b.workerToolBudgetFor
	b.workflowMgr.EvidenceDigests = append(b.workflowMgr.EvidenceDigests, scan.WorkflowEvidenceDigest(b.scanStore))
	return b.wireWorkflowComposition()
}

func (b boardWiring) wireWorkflowComposition() error {
	obligationSpecs := workflow.ObligationSpecsFromKinds(b.workflowMgr.Obligations)
	b.workflowComposer = &workflow.Composer{
		ModuleRoot:   b.catalog.ModuleRoot,
		SessionStore: b.sessionWorkflowStore,
		Registry:     b.condReg,
		Obligations:  obligationSpecs,
		Agents:       b.agents.Registry,
	}
	if composePolicy, err := workflow.LoadComposePolicy(); err != nil {
		return fmt.Errorf("compose policy: %w", err)
	} else {
		b.workflowComposer.Policy = composePolicy
	}
	// Tests may replace the effective template catalog with fixtures.
	loadTemplates := func() (workflow.TemplateCatalog, error) {
		if b.startup.cfg.TestWorkflowTemplatesDir != "" {
			return workflow.LoadTemplatesFromDir(extpacks.OnDisk(b.startup.cfg.TestWorkflowTemplatesDir))
		}
		return workflow.LoadTemplatesEffective(b.catalog.Effective)
	}
	if templates, err := loadTemplates(); err != nil {
		return fmt.Errorf("workflow templates: %w", err)
	} else {
		b.workflowComposer.Templates = templates
	}
	b.workflowPersister = &workflow.Persister{
		ModuleRoot:   b.catalog.ModuleRoot,
		SessionStore: b.sessionWorkflowStore,
		Registry:     b.condReg,
		Obligations:  obligationSpecs,
		Agents:       b.agents.Registry,
		Policy:       b.workflowComposer.Policy,
	}

	ruleEngine, err := rules.NewPostureRuleEngine(b.agents.Postures, b.bundledRules, b.condReg)
	if err != nil {
		return fmt.Errorf("posture rule engine: %w", err)
	}
	b.ruleEngine = ruleEngine
	b.projectRulesOverlay = rules.NewProjectRulesOverlay(b.condReg)
	b.ruleEngine.Overlay = b.projectRulesOverlay
	b.ruleEngine.ProjectSettingsApply = b.settings.ProjectSurfaceGate(projectcontrib.SurfaceProjectSettings, b.storage.Projects).Applies
	return nil
}
