package app

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/app/delegations"
	"github.com/lycaon/lycaon/internal/app/scanning"
	"github.com/lycaon/lycaon/internal/app/workflows"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
	"github.com/lycaon/lycaon/pkg/api"
)

func (b *serveBuilder) wireWorkflows() error {
	b.delegations = delegations.New(b.storage.Database, b.events.Outbox, b.worker.cfg)
	b.sessions.Manager.Coordinator.Closeout.SetDelegations(b.delegations.Store)

	var err error
	b.workflows, err = workflows.Build(b.startup.ctx, workflows.Dependencies{
		Database:            b.storage.Database,
		DataDir:             b.storage.Directory,
		ModuleRoot:          b.catalog.ModuleRoot,
		EffectiveCatalog:    b.catalog.Effective,
		Projects:            b.storage.Projects,
		Sessions:            b.storage.Sessions,
		SessionManager:      b.sessions.Manager,
		DelegationStore:     b.delegations.Store,
		EventsOutbox:        b.events.Outbox,
		EventPublisher:      b.events.Publisher,
		AuthzRecorder:       b.security.Authority.Recorder,
		WebResearchConfig:   nil,
		ProjectSettingsGate: b.settings.ProjectSurfaceGate(projectcontrib.SurfaceProjectSettings, b.storage.Projects),
	}, b.registerRecovery)
	if err != nil {
		return err
	}

	b.sessions.Manager.Verification.SetEvidenceStore(b.workflows.Evidence)
	if b.settings.Service != nil {
		b.sessions.Manager.Verification.SetVerifyConfig(b.settings.Service.Verify)
		if b.execution.Host != nil {
			b.execution.Host.Commands.SetVerifyDeclaredCommand(b.settings.Service.Verify.VerifyTestCommand)
		}
	}
	return b.wireWorkflowScanServices()
}

func (b *serveBuilder) wireWorkflowScanServices() error {
	b.git.mgr = git.NewManager()
	gitexec.SetHostConfig(gitexec.HostConfigResolver())
	b.git.status = git.NewStatusCache(b.git.mgr)
	releaseGitStatus := b.git.status.RegisterRepochangeObserver()
	b.startup.resources.Track("git-status-observer", 22, func(context.Context) error { releaseGitStatus(); return nil })
	b.git.repoSets = git.NewRepoSetCache(git.DefaultStatusCacheTTL)
	if b.execution.Host != nil {
		b.execution.Host.Survey.SetGitStatusCache(b.git.status)
	}

	snapshotStore := b.storage.SourceLedger.Snapshots

	var err error
	b.scanning, err = scanning.Build(b.startup.ctx, scanning.Dependencies{
		Database:              b.storage.Database,
		DataDir:               b.storage.Directory,
		ModuleRoot:            b.catalog.ModuleRoot,
		Events:                b.events.Outbox,
		Publisher:             b.events.Publisher,
		GitMgr:                b.git.mgr,
		Snapshots:             snapshotStore,
		Settings:              b.settings.Service,
		Projects:              b.storage.Projects,
		SurfaceGate:           b.settings.ProjectSurfaceGate(projectcontrib.SurfaceScanConfig, b.storage.Projects),
		EvidenceStore:         b.workflows.Evidence,
		SimpleInspector:       b.workflows.Inspector,
		FingerprintScannerKey: b.security.Fingerprinter.ScannerKey,
		ScanIgnores:           b.security.ScanIgnores,
		InjectRenderer:        b.delegations.InjectRenderer,
		TestRegistry:          b.startup.cfg.TestScanRegistry,
		DelegationBySession: func(sessionID string) (string, string, bool) {
			delegationID, ok := b.delegations.Store.DelegationBySessionID(sessionID)
			if !ok {
				return "", "", false
			}
			return delegationID, "", true
		},
		WorkflowRunsGet:   b.workflows.Store.Runs.Get,
		WorkflowRunParams: b.workflows.Manager.Obligations.ObligationParams,
		OnDelta: func(ctx context.Context, completed api.CodeScan, introduced, fixed []api.SecurityFinding) {
			if completed.TargetKind == api.ScanTargetPaths {
				b.sessions.Manager.Coordinator.Scans.Delta(ctx, completed, introduced, fixed)
			}
		},
		OnScanDone: func(ctx context.Context, completed api.CodeScan) {
			b.sessions.Manager.Coordinator.Scans.Finished(ctx, completed)
		},
		RetryCloseout: func(ctx context.Context, delegationID string) error {
			if b.delegations != nil && b.delegations.Manager != nil {
				return b.delegations.Manager.RetryCloseout(ctx, delegationID)
			}
			return nil
		},
		WorkflowTerminals: func(ctx context.Context, scanID string) error {
			return scanning.ReconcileTerminals(ctx, b.scanning.Store, scanID, b.workflows.Manager.Obligations.RecordObligationTerminal)
		},
		BindMovedFiles: func(ctx context.Context, completed api.CodeScan) error {
			runs, lErr := b.workflows.Store.Runs.ListRunning(ctx)
			if lErr != nil {
				return lErr
			}
			return scanning.BindMovedFiles(ctx, b.scanning.Store, runs, completed)
		},
	})
	if err != nil {
		return err
	}

	b.workflows.Manager.Coverage.Inventory = workflowScanInventory{store: b.scanning.Store}
	b.sessions.Manager.SetReportDocumentChecker(b.workflows.Manager.Reports)
	b.sessions.Manager.Coordinator.Scans.Evidence = b.workflows.Manager.Coverage

	b.events.BindWorkers(b.delegations.Queue)
	b.delegations.Queue.SetWorkflowDomains(&worker.WorkflowDomains{Runs: b.workflows.Manager.Policy, Tasks: b.workflows.Manager.Fanout})
	b.workflows.Store.Transactions.SetWorkerRunnableNotifier(b.delegations.Queue)

	if err := b.seedHostPowerWork(); err != nil {
		return err
	}

	return b.wireWorkflowConditions()
}

func (b *serveBuilder) seedHostPowerWork() error {
	openWorkers, err := b.delegations.Queue.List(b.startup.ctx, "", api.WorkerStatusPending, api.WorkerStatusRunning)
	if err != nil {
		return fmt.Errorf("seed host power from workers: %w", err)
	}
	for i := range openWorkers {
		b.settings.Power.SetActive("worker:"+openWorkers[i].ID, true)
	}
	openScans, err := b.scanning.Store.ListOpen(b.startup.ctx)
	if err != nil {
		return fmt.Errorf("seed host power from scans: %w", err)
	}
	for i := range openScans {
		b.settings.Power.SetActive("scan:"+openScans[i].ID, true)
	}
	return nil
}

func (b *serveBuilder) wireWorkflowConditions() error {
	var scannersEnabled func() bool
	if b.settings.Service != nil && b.settings.Service.Review != nil {
		scannersEnabled = func() bool { return b.settings.Service.SecurityScanners.Effective().Enabled }
	}
	err := b.workflows.BuildConditions(b.startup.ctx, workflows.ConditionDependencies{
		ModuleRoot:              b.catalog.ModuleRoot,
		DelegationStore:         b.delegations.Store,
		ScanStore:               b.scanning.Store,
		ScanObligation:          b.scanning.Obligation,
		Snapshots:               b.storage.SourceLedger.Snapshots,
		GatesCfg:                b.scanning.GatesCfg,
		SecurityScannersEnabled: scannersEnabled,
		Checkpoints:             b.sessions.Checkpoints,
		WorkerQueue:             b.delegations.Queue,
		Agents:                  b.agents.Registry,
		Postures:                b.agents.Postures,
		EffectiveCatalog:        b.catalog.Effective,
		TestTemplatesDir:        b.startup.cfg.TestWorkflowTemplatesDir,
		ProjectSettingsGate:     b.settings.ProjectSurfaceGate(projectcontrib.SurfaceProjectSettings, b.storage.Projects),
		SourceVerifyPassed:      b.sessions.Manager.Verification.WorkflowSourceVerifyPassed,
		DeliveryReported:        b.sessions.Manager.Runner.Transcript.DeliveredWorkflowPhase,
	})
	if err != nil {
		return err
	}

	if b.settings.Service != nil && b.settings.Service.Review != nil {
		contentApply := &toolhost.ContentApplyService{
			Mgr:    b.sessions.Checkpoints,
			Review: b.settings.Service.Review,
			PhaseSrc: &workflow.PhaseContentReviewSource{
				Runs: b.workflows.Manager.Policy,
			},
		}
		b.execution.Host.Mutations.SetContentApply(contentApply)
	}
	if b.execution.Host != nil && b.workflows.Manager != nil {
		releaseBlueprintObserver := b.execution.Host.Mutations.SetBlueprintWriteObserver(b.workflows.Manager.Blueprints)
		b.startup.resources.Track("blueprint-write-observer", 22, releaseBlueprintObserver)
	}
	b.workflows.Manager.Obligations.Register(b.scanning.Obligation)
	workflowTaskQuery1 := func(ctx context.Context, runID string) ([]api.WorkerTask, error) {
		return b.delegations.Queue.ListByWorkflowRunID(ctx, runID)
	}
	b.workflows.Manager.Fanout.WorkerTasks = workflowTaskQuery1
	b.workflows.Manager.Coverage.WorkerTasks = workflowTaskQuery1
	b.workflows.Manager.Verdicts.Questions.WorkerTasks = workflowTaskQuery1
	b.workflows.Manager.Verdicts.WorkerTasks = workflowTaskQuery1
	b.workflows.Manager.Fanout.WorkerToolBudget = b.sessions.WorkerToolBudgetFor
	b.security.BindSpawnAgents(b.workflows.Manager.Policy.AllowedAgents)
	b.workflows.Manager.Verdicts.Evidence.EvidenceDigests = append(b.workflows.Manager.Verdicts.Evidence.EvidenceDigests, scan.WorkflowEvidenceDigest(b.scanning.Store))
	return nil
}
